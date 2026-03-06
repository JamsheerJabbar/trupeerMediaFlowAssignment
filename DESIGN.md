# MediaFlow — Architecture Design Decisions

This document explains every significant design choice made in the MediaFlow system:
what was chosen, why it was chosen, what was traded away, and how each layer scales
from a single `docker-compose up` to a production deployment.

---

## System Architecture Diagram

<details open>
<summary>Click to expand diagram</summary>

<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>MediaFlow Architecture</title>
  <style>
    :root {
      --bg-color: #f8fafc; --text-main: #0f172a; --text-muted: #475569;
      --font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      --color-api-bg: #eff6ff; --color-api-border: #bfdbfe; --color-api-accent: #2563eb;
      --color-db-bg: #faf5ff; --color-db-border: #e9d5ff; --color-db-accent: #9333ea;
      --color-broker-bg: #fff1f2; --color-broker-border: #fecdd3; --color-broker-accent: #e11d48;
      --color-worker-bg: #fffbeb; --color-worker-border: #fde68a; --color-worker-accent: #d97706;
      --color-storage-bg: #f0fdf4; --color-storage-border: #bbf7d0; --color-storage-accent: #16a34a;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { font-family: var(--font-family); background-color: var(--bg-color); color: var(--text-main); line-height: 1.5; padding: 2rem; }
    .container { max-width: 1200px; margin: 0 auto; }
    .header { margin-bottom: 2.5rem; border-bottom: 2px solid #e2e8f0; padding-bottom: 1.5rem; }
    .header h1 { font-size: 2rem; font-weight: 700; color: #0f172a; margin-bottom: 0.5rem; }
    .header p { color: var(--text-muted); font-size: 1.1rem; }
    .architecture-grid { display: flex; flex-direction: column; gap: 2rem; position: relative; }
    .tier { background: white; border: 1px solid #cbd5e1; border-radius: 12px; padding: 1.5rem; box-shadow: 0 1px 3px rgba(0,0,0,0.05); position: relative; }
    .tier-title { font-size: 1rem; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 1.25rem; display: flex; align-items: center; gap: 0.5rem; }
    .cards-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1.5rem; }
    .card { border: 2px solid; border-radius: 8px; padding: 1.25rem; display: flex; flex-direction: column; gap: 0.75rem; }
    .card-title { font-size: 1.1rem; font-weight: 700; }
    .card-desc { font-size: 0.9rem; color: var(--text-muted); }
    .theme-api { background: var(--color-api-bg); border-color: var(--color-api-border); }
    .theme-api .card-title { color: var(--color-api-accent); }
    .theme-db { background: var(--color-db-bg); border-color: var(--color-db-border); }
    .theme-db .card-title { color: var(--color-db-accent); }
    .theme-broker { background: var(--color-broker-bg); border-color: var(--color-broker-border); }
    .theme-broker .card-title { color: var(--color-broker-accent); }
    .theme-worker { background: var(--color-worker-bg); border-color: var(--color-worker-border); }
    .theme-worker .card-title { color: var(--color-worker-accent); }
    .theme-storage { background: var(--color-storage-bg); border-color: var(--color-storage-border); }
    .theme-storage .card-title { color: var(--color-storage-accent); }
    .tags { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-top: auto; }
    .tag { font-size: 0.75rem; font-weight: 600; padding: 0.25rem 0.5rem; border-radius: 4px; background: rgba(0,0,0,0.05); color: #334155; }
    .badge { display: inline-block; font-size: 0.75rem; font-family: monospace; font-weight: 600; padding: 2px 6px; border-radius: 4px; color: white; }
    .badge-post { background: #10b981; } .badge-get { background: #3b82f6; } .badge-patch { background: #f59e0b; }
    ul.feature-list { list-style: none; font-size: 0.85rem; display: flex; flex-direction: column; gap: 0.4rem; }
    ul.feature-list li::before { content: "•"; color: currentColor; font-weight: bold; margin-right: 0.5rem; }
    .prod-path { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px dashed rgba(0,0,0,0.1); font-size: 0.8rem; color: var(--text-muted); display: flex; align-items: center; gap: 0.4rem; }
    .prod-path strong { color: var(--text-main); font-weight: 600; }
    .down-arrow { text-align: center; color: #94a3b8; font-size: 1.5rem; margin: -1rem 0; z-index: 10; }
    .split-layout { display: grid; grid-template-columns: 2fr 1fr; gap: 2rem; }
    .lifecycle-panel { background: white; border: 1px solid #cbd5e1; border-radius: 12px; padding: 1.5rem; }
    .lifecycle-step { font-size: 0.85rem; margin-bottom: 1rem; padding-left: 1.25rem; position: relative; border-left: 2px solid #e2e8f0; }
    .lifecycle-step::before { content: ""; position: absolute; left: -6px; top: 4px; width: 10px; height: 10px; border-radius: 50%; background: #3b82f6; }
    .lifecycle-step strong { color: #0f172a; display: block; margin-bottom: 0.2rem; }
    @media (max-width: 900px) { .split-layout { grid-template-columns: 1fr; } }
  </style>
</head>
<body>
<div class="container">
  <header class="header">
    <h1>MediaFlow Architecture</h1>
    <p>A distributed, fault-tolerant media processing system built for scale. Runs entirely with <code>docker-compose up</code>.</p>
  </header>
  <div class="split-layout">
    <div class="architecture-grid">
      <div class="tier">
        <div class="tier-title">1. Control Plane & Entrypoint</div>
        <div class="cards-row">
          <div class="card theme-api">
            <div class="card-title">API Gateway (FastAPI)</div>
            <div class="card-desc">Handles client requests, job state orchestration, and pre-flight worker checks.</div>
            <ul class="feature-list">
              <li><span class="badge badge-post">POST</span> /jobs (Submit job)</li>
              <li><span class="badge badge-get">GET</span> /jobs/{id} (Poll status)</li>
              <li><span class="badge badge-patch">PATCH</span> /internal/stages/{id} (Worker callbacks)</li>
            </ul>
          </div>
          <div class="card theme-api">
            <div class="card-title">Background Monitors</div>
            <div class="card-desc">Asynchronous safety nets running inside the Gateway.</div>
            <ul class="feature-list">
              <li><strong>Watchdog (60s):</strong> Re-queues stale <em>in_progress</em> jobs caused by silent worker crashes.</li>
              <li><strong>Queue Monitor (30s):</strong> Tracks queue depth vs worker capacity for alerting.</li>
            </ul>
          </div>
          <div class="card theme-db">
            <div class="card-title">SQLite Database</div>
            <div class="card-desc">Source of truth for job states. WAL mode enabled for concurrent reads/writes.</div>
            <ul class="feature-list">
              <li>Tracks <code>status</code>, <code>attempt_count</code>, paths</li>
              <li>Aggregates stage status to job level</li>
            </ul>
            <div class="prod-path"><strong>Production:</strong> Drop-in replace with PostgreSQL via env var.</div>
          </div>
        </div>
      </div>
      <div class="down-arrow">↓</div>
      <div class="tier">
        <div class="tier-title">2. Message Broker Transport</div>
        <div class="cards-row">
          <div class="card theme-broker">
            <div class="card-title">Redis</div>
            <div class="card-desc">Decouples web requests from processing. Ensures jobs aren't lost during traffic spikes.</div>
            <ul class="feature-list">
              <li><strong>Queues:</strong> <code>queue:overlay</code>, <code>transcode</code>, <code>extract</code> (LIST)</li>
              <li><strong>Presence:</strong> <code>worker:presence:{type}:{id}</code> (15s TTL)</li>
              <li><strong>DLQ:</strong> Jobs fail after 3 retries move to <code>dlq:*</code></li>
            </ul>
            <div class="prod-path"><strong>Production:</strong> Upgrade to Redis Streams + Consumer Groups.</div>
          </div>
        </div>
      </div>
      <div class="down-arrow">↓</div>
      <div class="tier">
        <div class="tier-title">3. Scalable Worker Pools (Stateless Processors)</div>
        <div class="cards-row">
          <div class="card theme-worker">
            <div class="card-title">worker-overlay</div>
            <div class="card-desc">Burns .srt subtitles into video.</div>
            <ul class="feature-list"><li>Tool: <code>FFmpeg</code></li><li>Capacity: 2 Concurrent jobs per container</li></ul>
            <div class="tags"><span class="tag">Docker Scalable</span></div>
          </div>
          <div class="card theme-worker">
            <div class="card-title">worker-transcode</div>
            <div class="card-desc">Downscales video to 480p (H.264/AAC).</div>
            <ul class="feature-list"><li>Tool: <code>FFmpeg</code></li><li>Capacity: 1 Concurrent job (CPU Heavy)</li></ul>
            <div class="tags"><span class="tag">Docker Scalable</span></div>
          </div>
          <div class="card theme-worker">
            <div class="card-title">worker-extract</div>
            <div class="card-desc">Strips audio & encodes to MP3.</div>
            <ul class="feature-list"><li>Tool: <code>FFmpeg</code></li><li>Capacity: 3 Concurrent jobs (Lightweight)</li></ul>
            <div class="tags"><span class="tag">Docker Scalable</span></div>
          </div>
        </div>
        <div class="prod-path" style="margin-top: 1rem; border-top: none; padding-top: 0;"><strong>Production:</strong> Replace manual docker-compose scaling with Kubernetes KEDA (HPA based on Redis queue depth).</div>
      </div>
      <div class="down-arrow">↓</div>
      <div class="tier">
        <div class="tier-title">4. Object Storage & Idempotency</div>
        <div class="cards-row">
          <div class="card theme-storage">
            <div class="card-title">MinIO Storage</div>
            <div class="card-desc">Stores raw inputs and processed outputs. Output paths act as native idempotency guards.</div>
            <ul class="feature-list">
              <li>Input: <code>jobs/{id}/input/original.mp4</code></li>
              <li>Output: <code>jobs/{id}/stages/{stage_id}/output.*</code></li>
              <li><em>If worker crashes post-upload, next retry sees output exists and skips processing.</em></li>
            </ul>
            <div class="prod-path"><strong>Production:</strong> Switch credentials/endpoint to AWS S3 or GCS.</div>
          </div>
        </div>
      </div>
    </div>
    <div class="lifecycle-panel">
      <h2 style="font-size: 1.1rem; margin-bottom: 1.5rem; color: #0f172a;">Job Lifecycle Flow</h2>
      <div class="lifecycle-step"><strong>1. Submit Job</strong>Client sends media to API Gateway. Gateway checks worker presence, logs Job & Stages to SQLite, uploads to MinIO, and pushes messages to Redis.</div>
      <div class="lifecycle-step"><strong>2. Worker Claims</strong>Worker's BRPOP loop wakes up. Patches Gateway to set status to <code>in_progress</code>.</div>
      <div class="lifecycle-step"><strong>3. Idempotency Check</strong>Worker checks MinIO. If output exists (due to prior crash), it skips processing entirely.</div>
      <div class="lifecycle-step"><strong>4. Processing</strong>Downloads input, processes via FFmpeg, uploads output to MinIO.</div>
      <div class="lifecycle-step"><strong>5. Completion & Retry</strong>Worker patches Gateway with <code>completed</code> or <code>failed</code>. Gateway increments attempt count on failure. After 3 tries, moves to Dead Letter Queue (DLQ).</div>
      <div class="lifecycle-step"><strong>6. Fault Tolerance</strong>If a worker dies silently during step 4, the Gateway's Watchdog catches the timeout and re-queues the job seamlessly.</div>
    </div>
  </div>
</div>
</body>
</html>

</details>

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