# MediaFlow — Architecture Design Decisions

This document explains every significant design choice made in the MediaFlow system:
what was chosen, why it was chosen, what was traded away, and how each layer scales
from a single `docker-compose up` to a production deployment.

---

## System Architecture Diagram

flowchart TD
    Client(["🖥️ Client"])

    subgraph GW["API Gateway"]
        direction TB
        HTTP["HTTP Server\nPOST /jobs · GET /jobs/:id\nPATCH /internal/stages/:id\nGET /admin/dlq/:type"]
        WD["⏱ Watchdog\nevery 60s — re-queues stale in_progress stages"]
        QM["📊 Queue Monitor\nevery 30s — queue depth vs worker capacity"]
    end

    subgraph DB["SQLite · WAL Mode"]
        direction TB
        JT["jobs\nid · status · timestamps"]
        ST["stages\nid · job_id · type · status\nattempt_count · output_path · error"]
    end

    subgraph RD["Redis"]
        direction TB
        Q1["queue:overlay  LIST"]
        Q2["queue:transcode  LIST"]
        Q3["queue:extract  LIST"]
        DLQ["dlq:overlay / transcode / extract"]
        HP["worker:presence:{type}:{id}\nTTL 15s · refreshed every 10s"]
    end

    subgraph WK["Worker Pools · Stateless"]
        direction LR
        WO["worker-overlay\nFFmpeg — SRT burn\n2 concurrent"]
        WT["worker-transcode\nFFmpeg — 480p H.264\n1 concurrent"]
        WE["worker-extract\nFFmpeg — MP3\n3 concurrent"]
    end

    subgraph MN["MinIO · S3-Compatible Storage"]
        IN["jobs/{id}/input/original.mp4"]
        OUT["jobs/{id}/stages/{stage_id}/output.*\nDeterministic path = idempotency guard"]
    end

    Client -->|"POST /jobs multipart upload"| HTTP
    Client -->|"GET /jobs/:id poll status"| HTTP

    HTTP --> DB
    HTTP -->|"LPUSH on submit"| RD
    WD -->|"reads stale stages"| DB
    WD -->|"LPUSH re-queue or DLQ"| RD
    QM -->|"LLEN + KEYS presence"| RD
    HTTP -->|"upload input"| MN

    Q1 -->|BRPOP| WO
    Q2 -->|BRPOP| WT
    Q3 -->|BRPOP| WE

    WO <-->|"download input / upload output"| MN
    WT <-->|"download input / upload output"| MN
    WE <-->|"download input / upload output"| MN

    WO -->|"PATCH completed or failed"| HTTP
    WT -->|"PATCH completed or failed"| HTTP
    WE -->|"PATCH completed or failed"| HTTP

    WO -->|"heartbeat SET TTL"| HP
    WT -->|"heartbeat SET TTL"| HP
    WE -->|"heartbeat SET TTL"| HP

    style GW fill:#eff6ff,stroke:#bfdbfe,color:#1e3a5f
    style DB fill:#faf5ff,stroke:#e9d5ff,color:#3b1f5e
    style RD fill:#fff1f2,stroke:#fecdd3,color:#7f1d1d
    style WK fill:#fffbeb,stroke:#fde68a,color:#713f12
    style MN fill:#f0fdf4,stroke:#bbf7d0,color:#14532d

---

## 1. Gateway Orchestration

### What We Built

The API Gateway is a single HTTP server process with three responsibilities running concurrently inside it: request handling, the watchdog loop, and the queue monitor loop. It owns all control-plane logic — job creation, retry decisions, DLQ routing, and pre-flight worker availability checks. Workers are deliberately kept dumb: they do work, report outcomes, and nothing else.

**Status polling is client-driven (pull model).** After submitting a job, the client receives a `job_id` and polls `GET /jobs/{job_id}` at its own cadence to check progress. The gateway reads from SQLite and returns the current state of all stages with their statuses and, on completion, presigned download URLs.

### Why Polling Over Push (Webhooks / WebSockets)

| | API Polling | Webhooks | WebSockets |
|---|---|---|---|
| Client complexity | Minimal | Moderate | High |
| Infrastructure | None extra | Requires reachable endpoint | Persistent connection |
| Reliability | Client controls retry | Delivery not guaranteed | Connection must survive job duration |
| Debuggability | Trivially inspectable | Async, harder to trace | Stateful, harder to trace |
| Local dev | Works out of the box | Requires tunneling (ngrok etc.) | Works, but heavyweight |

Polling was chosen because it is the simplest contract to implement correctly on both sides, requires no infrastructure beyond the gateway already present, and is trivially debuggable with any HTTP client. For a media processing system where jobs take seconds to minutes, the overhead of one GET every few seconds is negligible.

**The tradeoff accepted:** Polling introduces latency between job completion and client notification equal to the polling interval. For real-time scenarios (live transcription, sub-second pipelines) this is unacceptable.

### How to Scale the Gateway

**Horizontal scaling:** The gateway is stateless in its request-handling path — all state lives in SQLite and Redis. Multiple gateway instances can run behind a load balancer as long as they share the same SQLite file (via a network volume) or, preferably, a PostgreSQL instance.

**The background loop problem:** The watchdog and queue monitor run as in-process async tasks. If three gateway instances are running, three watchdog loops run simultaneously — all scanning the same stale stages and potentially re-queuing the same job three times.

Fix: Add a distributed lock before the watchdog cycle. Before scanning for stale stages, the gateway attempts to acquire a Redis lock with a TTL slightly shorter than the watchdog interval. Only the instance that holds the lock runs the cycle. The others skip it cleanly. This is a standard leader-election pattern using `SET NX EX`.

```
WATCHDOG LOCK KEY:  watchdog:leader
TTL:                55 seconds (watchdog interval is 60s)
Acquire:            SET watchdog:leader {instance_id} NX EX 55
If acquired:        run the watchdog cycle
If not acquired:    skip this cycle, try again next interval
```

**Production scaling path:**

| Layer | Local | Production |
|---|---|---|
| Instances | 1 gateway container | 3+ replicas behind load balancer |
| Background loops | In-process async tasks | Distributed lock (Redis NX) or separate cron service |
| Status delivery | Client polling | Add webhook callbacks or SSE for completion events |
| Auth | None | JWT / API key middleware at gateway |

---

## 2. Redis as Message Broker

### What We Built

Each job type has a dedicated Redis LIST (`queue:overlay`, `queue:transcode`, `queue:extract`). Workers consume from their list using `BRPOP` — a blocking pop that sleeps at the OS level until a message arrives, then wakes exactly one waiting worker. Gateway enqueues with `LPUSH`. Failed jobs past the retry limit go to a corresponding DLQ list (`dlq:overlay` etc.).

Worker heartbeats are stored as Redis STRING keys with a 15-second TTL, refreshed every 10 seconds. This gives the gateway a live view of which workers are present, used in pre-flight checks at job submission time.

### Why Redis

Redis was chosen over alternatives (RabbitMQ, Kafka, SQS, in-memory queue) for these reasons:

**It serves double duty.** The same Redis instance used for queuing also stores worker presence keys, DLQ lists, and (optionally) distributed locks. No second infrastructure component is needed.

**BRPOP is purpose-built for this pattern.** A blocking pop on a LIST is the exact primitive needed for a worker pool: multiple workers block on the same key, Redis wakes exactly one when a message arrives, and the rest stay sleeping. No polling, no spin-loops.

**Operationally trivial locally.** A single `redis:7-alpine` container. No cluster setup, no topic configuration, no schema registry.

### Tradeoffs Accepted

**At-most-once delivery risk.** `BRPOP` is a destructive read — the moment a worker pops a message, it is gone from the list. If the worker crashes before reporting back to the gateway, the message is lost from Redis. The system recovers this via the watchdog (which detects stale `in_progress` stages in SQLite and re-enqueues them), but there is a window between the pop and the watchdog scan where the job is in-flight with no tracking. The watchdog interval (default 60 seconds) is the maximum exposure window.

**No native consumer group semantics.** If two workers are both waiting on `BRPOP queue:overlay`, Redis wakes one of them — but there is no formal concept of acknowledgment. The job is considered "taken" the moment it leaves the list, not when work is confirmed complete.

**Key-scan for presence is O(N).** `KEYS worker:presence:overlay:*` scans all Redis keys. With many workers this degrades. Replace with `SCAN` cursor or maintain a Redis SET of active worker IDs alongside TTL keys.

### Production Upgrade: Redis Streams with Consumer Groups

Redis Streams (via `XREADGROUP`) solve the at-most-once problem natively:

```
queue:overlay (Stream)
  Consumer Group: overlay-workers
    Consumer worker-1: XREADGROUP → message delivered but NOT removed
    Consumer worker-1: XACK       → message removed after confirmed processing

Crash recovery:
  If worker-1 crashes without XACK → message stays in Pending Entry List (PEL)
  XAUTOCLAIM after timeout → reassigns message to another consumer automatically
```

This makes the transport layer self-healing. The watchdog still handles DB-level stale detection, but transport-level crash recovery no longer requires scanning SQLite. The watchdog becomes a secondary safety net rather than the primary recovery mechanism.

**Migration path:** Redis Streams use a different API (`XADD`, `XREADGROUP`, `XACK`) but the same Redis instance and connection. The queue service module is the only code that changes — all other layers are unaffected.

| | Redis LIST + BRPOP | Redis Streams + Consumer Groups |
|---|---|---|
| Delivery | At-most-once | At-least-once |
| Crash recovery | Watchdog (60s window) | XAUTOCLAIM (configurable, seconds) |
| DLQ | Manual LPUSH to dlq: key | Native dead-letter via max delivery count |
| Replay | Manual LRANGE on DLQ | Native stream rewind |
| Complexity | Low | Moderate |

---

## 3. SQLite for Job State

### What We Built

All job and stage state lives in a SQLite database stored on a named Docker volume. WAL (Write-Ahead Logging) mode is enabled, which allows concurrent reads alongside writes without serializing them. The schema is two tables — `jobs` and `stages` — with an index on `stages(status, updated_at)` specifically to make the watchdog's stale-stage query efficient.

SQLite is the gateway's source of truth. Workers do not access it directly — they report outcomes to the gateway via HTTP, and the gateway writes to SQLite. This means SQLite has exactly one writer process, which is the primary constraint WAL mode works around (WAL handles concurrent readers against a single writer gracefully).

### Why SQL (and Why SQLite Locally)

**SQL was chosen over a document store or key-value store** because the job state has inherently relational structure: one job has many stages, stages have foreign keys to jobs, and the watchdog query is a filtered scan across the stages table with a compound predicate (`status = 'in_progress' AND type = X AND updated_at < threshold`). This query maps directly to an indexed SQL predicate. In a document store it would require a full collection scan or a maintained secondary index.

**SQLite specifically** because it runs as a library inside the gateway process — no separate container, no network round-trip, no connection pool to manage. For a single-writer workload behind one gateway instance, SQLite with WAL mode matches PostgreSQL performance for this access pattern while adding zero operational overhead.

### Tradeoffs Accepted

**Single-node only.** SQLite is a file, not a server. It cannot be accessed concurrently by multiple gateway instances pointing at separate hosts. If you scale the gateway to three instances, they cannot share a SQLite file unless they all mount the same network filesystem volume — which reintroduces a network bottleneck and file-locking contention.

**No connection pooling semantics.** SQLite supports one writer at a time. WAL mode allows readers to proceed concurrently, but write contention under high throughput (many simultaneous job submissions) will serialize. For the expected load locally this is not an issue; at scale it is a hard limit.

**No built-in replication or failover.** If the volume containing the SQLite file is lost, all job state is lost. MinIO still has the media files; the state records are gone.

### How to Scale: PostgreSQL Migration

The migration is a connection string change. The schema is written to be PostgreSQL-compatible: no SQLite-specific types, no SQLite-specific syntax. `datetime('now')` becomes `NOW()`, but this is an abstraction handled at the database layer.

```
Local:      DATABASE_PATH=/data/mediaflow.db     (SQLite file)
Production: DATABASE_URL=postgresql://user:pass@host:5432/mediaflow
```

**Production database architecture:**

| Concern | Approach |
|---|---|
| Write throughput | Connection pooling via PgBouncer in transaction mode |
| Read throughput | Read replicas for GET /jobs polling (eventually consistent, acceptable) |
| Failover | Managed PostgreSQL (RDS, Cloud SQL, Supabase) with automatic failover |
| Watchdog query | The `stages(status, updated_at)` index translates directly — same query, same plan |
| Schema migrations | Add a migration tool (Flyway, Alembic, golang-migrate) before production |

**One nuance with the watchdog at scale:** At high job volume, the watchdog query touches potentially many rows. Partition the stages table by `status` or add a partial index:

```sql
CREATE INDEX idx_stages_stale ON stages(type, updated_at)
WHERE status = 'in_progress';
```

This makes the watchdog scan operate only on in-progress rows, which is a small fraction of the table at any given time.

---

## 4. MinIO as Object Storage

### What We Built

MinIO runs as a Docker container with a named volume. All media — input files and processed outputs — is stored here. The gateway uploads on job submission; workers download their input and upload their output independently. No media bytes flow through the gateway after the initial upload, and no media bytes are passed between workers directly.

Output paths are deterministic, keyed by `stage_id`:

```
jobs/{job_id}/stages/{stage_id}/output.mp4
```

This determinism is the idempotency mechanism. Before running FFmpeg, every worker checks whether this exact path already exists in MinIO. If it does, the previous worker completed the upload but crashed before reporting back. The current worker skips processing, reports completed, and the job moves forward. No double-processing, no wasted CPU.

### Why MinIO

**S3-compatible API.** MinIO implements the Amazon S3 API spec. The same SDK calls — `PutObject`, `GetObject`, `StatObject`, `PresignedGetObject` — work against both MinIO locally and AWS S3, GCS (via interoperability mode), or Azure Blob (via S3 compatibility layer) in production. Switching to production storage is an endpoint and credential change, not an API change.

**Self-contained.** Runs as a single Docker container with one named volume. No external service dependencies, no cloud credentials needed locally, no cost.

**Web console.** MinIO ships with a browser UI at port 9001. During development you can visually inspect the bucket, browse job outputs, and verify the path structure without writing any tooling.

**Presigned URLs.** MinIO generates time-limited signed URLs for objects, identical to S3 presigned URLs. This allows the gateway to return download links to clients without proxying the file bytes through the gateway itself. Large output files (a transcoded video, an audio track) go directly from storage to the client.

### Tradeoffs Accepted

**Not distributed by default locally.** The local MinIO setup is a single instance backed by a single volume. There is no replication, no erasure coding, no redundancy. If the volume fails, media is lost. This is acceptable locally; it is not acceptable in production.

**No lifecycle policies locally.** Job input and output files accumulate indefinitely. There is no TTL-based cleanup. For local development this is a non-issue. In production, uncleaned media storage is a cost and compliance concern.

**Memory pressure on large files.** The current worker implementation downloads the full input file into memory as bytes before writing to a temp file, and reads the full output into memory before uploading. For very large files (multi-GB video), this will exhaust worker container memory. The fix is streaming: pipe FFmpeg directly to and from MinIO using multipart upload without buffering the full file in memory.

### How to Scale: S3 Migration and Production Storage Architecture

**Immediate migration:**

```
MINIO_ENDPOINT=minio:9000        → s3.amazonaws.com (or region-specific)
MINIO_ACCESS_KEY=minioadmin      → IAM access key
MINIO_SECRET_KEY=minioadmin      → IAM secret key
MINIO_BUCKET=mediaflow           → your-production-bucket
```

No application code changes. The SDK is already S3-compatible.

**Production storage architecture:**

| Concern | Approach |
|---|---|
| Durability | S3 / GCS standard class — 11 nines durability, multi-AZ replication |
| Cost — hot storage | Use S3 Standard for active jobs (input + recent output) |
| Cost — cold storage | Lifecycle rule: transition outputs to S3 Glacier after 30 days |
| Cost — cleanup | Lifecycle rule: delete inputs after 7 days, outputs after 90 days |
| Access control | Pre-signed URLs with short TTL (1 hour) instead of 24 hours locally |
| Large file handling | Multipart upload for inputs > 100MB; streaming download to FFmpeg stdin |
| CDN | CloudFront / Cloud CDN in front of S3 for frequently accessed outputs |

**Lifecycle policy example (S3):**

```json
{
  "Rules": [
    {
      "Prefix": "jobs/",
      "Status": "Enabled",
      "Transitions": [
        { "Days": 30, "StorageClass": "GLACIER" }
      ],
      "Expiration": { "Days": 90 }
    }
  ]
}
```

This alone can reduce storage costs by 70–80% on a media processing workload where most files are accessed once and never again.

---

## Design Decision Summary

| Layer | Choice | Primary Reason | Production Upgrade |
|---|---|---|---|
| Gateway pattern | Single process + async loops | Simplicity, no extra containers | Distributed lock for multi-instance |
| Status delivery | Client polling | Zero infrastructure, trivially debuggable | Webhooks / SSE on completion |
| Message broker | Redis LIST + BRPOP | Dual-purpose (queues + presence), zero config | Redis Streams + Consumer Groups |
| Job state | SQLite + WAL | Single container, relational queries, zero ops | PostgreSQL + PgBouncer + read replicas |
| Object storage | MinIO | S3-compatible API, local web console | AWS S3 / GCS — credential swap only |
| Idempotency | Deterministic MinIO output paths | No locks, no coordination, naturally safe | Same mechanism works at any scale |
| Worker concurrency | BRPOP coroutines per container | Maps directly to OS concurrency, self-limiting | Same model in Kubernetes pods |
| Retry logic | Gateway-owned, attempt_count in SQLite | Single source of truth, no distributed coordination | Same — PostgreSQL replaces SQLite |