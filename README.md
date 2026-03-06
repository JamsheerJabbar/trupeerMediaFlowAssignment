# MediaFlow

A distributed media processing pipeline with video transcoding, audio extraction, and subtitle overlay capabilities.

## Local Test Commands

## Setup Docker Desktop up and running

```bash
# Start all services
docker-compose up --build

# Wait for services to be healthy (~30 seconds)
docker-compose ps
```

## API Usage

### Create a Job

**Extract audio from video:**
```bash
curl -X POST http://localhost:8000/jobs \
  -F "file=test_videos/NexoraDemo.mp4" \
  -F "stages=extract"
```

**Transcode video to 480p:**
```bash
curl -X POST http://localhost:8000/jobs \
  -F "file=test_videos/NexoraDemo.mp4" \
  -F "stages=transcode"
```

**Multiple stages:**
```bash
curl -X POST http://localhost:8000/jobs \
  -F "file=test_videos/NexoraDemo.mp4" \
  -F "stages=transcode,extract"
```

**Burn subtitles into video:**
```bash
curl -X POST http://localhost:8000/jobs \
  -F "file=test_videos/NexoraDemo.mp4" \
  -F "srt_file=test_subtitles/NexoraDemo.srt" \
  -F "stages=overlay"
```

### Check Job Status

```bash
curl http://localhost:8000/jobs/{job_id}
```

### Retry Failed Stages

```bash
curl -X POST http://localhost:8000/jobs/{job_id}/retry
```

### View Dead Letter Queue

```bash
curl http://localhost:8000/admin/dlq/overlay
curl http://localhost:8000/admin/dlq/transcode
curl http://localhost:8000/admin/dlq/extract
```

### Health Check

```bash
curl http://localhost:8000/health
```

## Run Integration Tests

```bash
./test_integration.sh --video-path ./test_videos/NexoraDemo.mp4
```

## Scale Workers

```bash
docker-compose up --scale worker-overlay=3 -d
docker-compose up --scale worker-transcode=2 -d
```

## View MinIO Console

Open http://localhost:9001 and login with `minioadmin` / `minioadmin`

## Shutdown

```bash
# Stop services (keep data)
docker-compose down

# Stop and delete all data
docker-compose down -v
```



# MediaFlow — Production Survival Guide

This document covers how MediaFlow is designed to survive and operate reliably in a
production environment. It is split into two concerns: **Observability** (how you know
something is wrong before your users do) and **Scale** (how the architecture changes
when job volume increases by 1000x).

---

## Part 1 — Observability & Alerting

### The Core Problem: You Cannot Watch Logs in Production

Locally, you `docker-compose logs -f` and see everything. In production, a job can
silently stall, a worker can enter a zombie state, and a queue can back up to 10,000
messages — all while the system reports healthy at the `/health` endpoint because the
gateway HTTP server is technically still accepting requests.

**Observability is the practice of making invisible system state visible and actionable
without human intervention.**

MediaFlow has three classes of failure that need dedicated detection strategies:

```
1. Zombie Worker     — worker is alive (heartbeat present) but making no progress
2. Stuck Job         — stage is in_progress but the responsible worker has vanished
3. System Pressure   — workers are alive but queue is growing faster than they can drain
```

Each requires a different signal, a different detection mechanism, and a different alert.

---

### Signal Layer: What Gets Measured

Every component in the system emits structured signals. The collection stack is:

```
Gateway + Workers
  │  structured JSON logs → Loki / CloudWatch Logs
  │  metrics endpoint     → Prometheus scrape every 15s
  └  traces               → OpenTelemetry → Jaeger / Tempo

Prometheus
  └── Grafana dashboards + alerting rules

Loki
  └── Grafana log explorer + log-based alerts
```

**Structured log format (every log line is JSON):**

```json
{
  "timestamp": "2025-03-06T14:23:01.442Z",
  "level": "warn",
  "service": "gateway",
  "component": "watchdog",
  "job_id": "a1b2c3d4",
  "stage_id": "e5f6g7h8",
  "stage_type": "transcode",
  "attempt": 2,
  "elapsed_seconds": 923,
  "event": "stale_stage_detected",
  "message": "Stage in_progress beyond timeout threshold, re-queuing"
}
```

Every log line carries `job_id`, `stage_id`, `worker_id` where applicable. This makes
log correlation trivial — searching for a single `job_id` surfaces every gateway log,
every worker log, and every retry event for that job across the entire cluster.

---

### Metrics: The Prometheus Endpoint

The gateway exposes `GET /metrics` returning Prometheus text format. These are the
metrics that power every dashboard and alert.

**Queue metrics (per job type):**

```
mediaflow_queue_depth{type="overlay"}        — current jobs waiting in Redis LIST
mediaflow_queue_depth{type="transcode"}
mediaflow_queue_depth{type="extract"}

mediaflow_dlq_depth{type="overlay"}          — dead-lettered jobs awaiting inspection
mediaflow_dlq_depth{type="transcode"}
mediaflow_dlq_depth{type="extract"}
```

**Worker metrics (per job type):**

```
mediaflow_live_workers{type="overlay"}       — count of valid heartbeat presence keys
mediaflow_live_workers{type="transcode"}
mediaflow_live_workers{type="extract"}

mediaflow_queue_utilization{type="overlay"}  — queue_depth / (live_workers × MAX_CONCURRENT_JOBS)
```

**Job metrics:**

```
mediaflow_jobs_submitted_total               — counter, incremented at POST /jobs
mediaflow_jobs_completed_total               — counter, incremented on all stages completed
mediaflow_jobs_failed_total                  — counter, incremented on dead_letter
mediaflow_stage_duration_seconds{type}       — histogram, time from in_progress → completed
mediaflow_stage_retries_total{type}          — counter, incremented each time a stage is requeued
```

**Gateway process metrics (auto-collected by Prometheus client library):**

```
http_request_duration_seconds{method, path, status}
http_requests_total{method, path, status}
process_cpu_seconds_total
process_resident_memory_bytes
```

---

### Detecting a Zombie Worker

A **zombie worker** is a worker process that is alive enough to keep sending heartbeats
(so its presence key in Redis stays fresh) but is not making progress on its current job.

This is distinct from a crashed worker (presence key expires) and from a slow job
(legitimate long-running processing). A zombie has a live heartbeat AND a job that
has been `in_progress` far beyond the expected duration for that job type.

**Detection mechanism — two-signal correlation:**

```
Signal 1:  worker:presence:{type}:{id}  key EXISTS in Redis  (worker appears alive)
Signal 2:  stage.updated_at < now() - (2 × TIMEOUT_{TYPE})  (job is double-overdue)
```

A stage that is past its timeout once triggers the watchdog re-queue (normal retry).
A stage that is past **double** the timeout — even after a watchdog re-queue — signals
that either the same worker keeps picking it up and stalling, or no worker is actually
processing it despite heartbeats saying otherwise. That is a zombie.

**Implementation — zombie detection query (runs in watchdog, logged separately):**

```sql
SELECT s.id, s.job_id, s.type, s.worker_id, s.updated_at, s.attempt_count
FROM stages s
WHERE s.status = 'in_progress'
  AND s.updated_at < datetime('now', '-' || (2 * timeout_seconds) || ' seconds')
  AND s.attempt_count >= 1
```

If this query returns rows, the system logs at `CRITICAL` level with `event: zombie_detected`
and the full stage and worker context. This log line triggers an alert (see Alert Rules below).

**Root causes a zombie alert should prompt you to investigate:**

- Worker OOM-killed mid-job but the heartbeat goroutine/thread survived on its own
- Worker stuck in an infinite FFmpeg retry loop that never exits
- Worker container clock skew causing heartbeat TTL miscalculation
- Redis connection issue causing heartbeat to fail silently while worker process lives

---

### Detecting a Stuck Job (Worker Vanished)

A **stuck job** is simpler: the stage is `in_progress`, the worker presence key for
the assigned `worker_id` has **expired** (not just stale — actually gone from Redis),
and the watchdog has not yet re-queued it.

The watchdog is the recovery mechanism, but the window between crash and watchdog
cycle (up to 60 seconds) is the visibility gap. An alert here fires before the watchdog
fires, giving you a heads-up that recovery is pending.

**Detection query (runs in queue monitor loop, not watchdog):**

```python
for stage in get_in_progress_stages():
    worker_key = f"worker:presence:{stage.type}:{stage.worker_id}"
    key_exists = await redis.exists(worker_key)
    
    if not key_exists:
        # Worker heartbeat has expired — worker is presumed dead
        elapsed = now() - stage.updated_at
        logger.warning(
            event="worker_vanished",
            stage_id=stage.id,
            job_id=stage.job_id,
            worker_id=stage.worker_id,
            elapsed_seconds=elapsed,
            message="Stage in_progress but assigned worker presence key has expired"
        )
```

This runs every 30 seconds (queue monitor interval). It fires a warning the moment
a worker's heartbeat expires, which is 15 seconds after the last successful heartbeat —
giving you a ~45 second end-to-end detection time before the 60-second watchdog cycle.

---

### Alert Rules

These are the Prometheus alert rules and log-based alerts that constitute a complete
production alerting setup. Each alert includes a severity, a description of what it
means operationally, and the first action to take.

---

**ALERT 1 — ZombieWorkerDetected**
```yaml
alert: ZombieWorkerDetected
expr: |
  # Log-based alert — trigger on any log line with event=zombie_detected
  # In Grafana: Loki alert on {service="gateway"} |= "zombie_detected"
severity: critical
for: 0m   # fire immediately, no wait
annotations:
  summary: "Zombie worker detected — worker alive but job making no progress"
  action: "Check worker container logs. Likely OOM or stuck subprocess. Restart worker."
```

---

**ALERT 2 — StageStuck**
```yaml
alert: StageStuck
expr: |
  # Log-based: {service="gateway"} |= "worker_vanished"
severity: warning
for: 2m   # wait 2 minutes — watchdog may already be handling it
annotations:
  summary: "Worker vanished with in_progress stage — awaiting watchdog recovery"
  action: "Informational. If persists > 5m, check watchdog loop is running."
```

---

**ALERT 3 — QueueDepthCritical**
```yaml
alert: QueueDepthCritical
expr: mediaflow_queue_depth > 100
for: 5m
severity: critical
labels:
  team: platform
annotations:
  summary: "Queue {{ $labels.type }} has {{ $value }} jobs backed up for 5 minutes"
  action: "Scale up worker-{{ $labels.type }} replicas. Check worker logs for errors."
```

---

**ALERT 4 — NoWorkersAvailable**
```yaml
alert: NoWorkersAvailable
expr: mediaflow_live_workers == 0
for: 1m
severity: critical
labels:
  page: true
annotations:
  summary: "Zero live workers for job type {{ $labels.type }}"
  action: "Immediate: check container health. New jobs are being rejected with 503."
```

---

**ALERT 5 — DLQDepthNonZero**
```yaml
alert: DLQNonEmpty
expr: mediaflow_dlq_depth > 0
for: 0m
severity: warning
annotations:
  summary: "{{ $value }} jobs dead-lettered for {{ $labels.type }}"
  action: "Inspect GET /admin/dlq/{{ $labels.type }}. Likely a bad input or FFmpeg error."
```

---

**ALERT 6 — HighRetryRate**
```yaml
alert: HighRetryRate
expr: rate(mediaflow_stage_retries_total[5m]) > 0.5
for: 3m
severity: warning
annotations:
  summary: "{{ $labels.type }} stages retrying at {{ $value }}/sec"
  action: "Systemic failure in {{ $labels.type }} workers. Check FFmpeg logs and input validation."
```

---

**ALERT 7 — GatewayLatencyHigh**
```yaml
alert: GatewayLatencyHigh
expr: histogram_quantile(0.95, http_request_duration_seconds_bucket{path="/jobs"}) > 2
for: 5m
severity: warning
annotations:
  summary: "p95 job submission latency is {{ $value }}s (threshold: 2s)"
  action: "Check MinIO upload latency and SQLite write contention."
```

---

**ALERT 8 — StageDurationAnomaly**
```yaml
alert: StageDurationAnomaly
expr: |
  histogram_quantile(0.95, mediaflow_stage_duration_seconds_bucket{type="transcode"})
  > 2 * histogram_quantile(0.50, mediaflow_stage_duration_seconds_bucket{type="transcode"})
for: 10m
severity: warning
annotations:
  summary: "p95 transcode duration is >2x the median — outlier jobs present"
  action: "Check input file sizes. May need to raise TIMEOUT_TRANSCODE or filter oversized inputs."
```

---

### Grafana Dashboard: What to Build

Four panels cover the system at a glance:

```
Row 1 — Queue Health
  [ Queue Depth per type — bar chart, 1m intervals ]
  [ DLQ Depth per type — stat panel, red if > 0 ]
  [ Queue Utilization per type — gauge, threshold at 0.8 ]

Row 2 — Worker Health
  [ Live Workers per type — stat panel, red if 0 ]
  [ Stage Duration p50/p95/p99 per type — line chart ]
  [ Retry Rate per type — line chart ]

Row 3 — Job Throughput
  [ Jobs submitted/completed/failed — counter time series ]
  [ Success rate (completed / submitted) — stat panel ]
  [ Time to completion per type — histogram heatmap ]

Row 4 — Gateway Process
  [ HTTP p95 latency per endpoint ]
  [ Error rate (5xx) ]
  [ Memory and CPU usage ]
```

---

### On-Call Runbook (Short Form)

When an alert fires, the first three questions are always:

```
1. Is the gateway healthy?
   → GET /health — check Redis connectivity
   → Check gateway container logs for panics or OOM

2. Are workers present?
   → GET /admin/dlq/{type} — any dead-lettered jobs?
   → redis-cli KEYS "worker:presence:*" — any presence keys?

3. Is the queue growing or draining?
   → redis-cli LLEN queue:overlay (and transcode, extract)
   → Compare against 5 minutes ago via Grafana
```

If workers are absent: restart worker containers, check resource limits (CPU/memory).
If workers are present but queue is growing: scale up replicas, check stage duration metrics for outlier jobs.
If DLQ is non-empty: inspect the error field on dead-lettered messages, likely a bad input file or FFmpeg configuration issue.

---

## Part 2 — Scaling from 10 to 10,000 Jobs/Hour

### Framing the Problem

At 10 jobs/hour the system is effectively idle most of the time. A single gateway
instance, one worker per type, and SQLite comfortably handle this load with headroom
to spare. At 10,000 jobs/hour the constraints are completely different:

```
10,000 jobs/hour = ~2.8 jobs/second submitted

If average transcode is 3 minutes:
  3 min × 2.8/sec = 504 concurrent transcode stages at steady state
  504 concurrent stages ÷ 1 job/container = 504 transcode containers

At extract (60s average):
  60s × 2.8/sec = 168 concurrent extract stages
```

The architecture does not need to be redesigned — the same components are used.
What changes is **where the bottlenecks are, and how each layer needs to be hardened.**

---

### Bottleneck Analysis by Layer

#### Bottleneck 1 — The Gateway Is a Single Point of Serialization

At 2.8 submissions/second, the gateway's `POST /jobs` handler must:
- Run a pre-flight Redis check
- Upload a file to MinIO
- Write to SQLite
- Push N messages to Redis

File upload latency alone (a 100MB video over a LAN) is 1–2 seconds. At 2.8/sec,
a single gateway instance saturates almost immediately. `GET /jobs/{id}` polling
multiplies this — at 10,000 jobs, even if each client polls every 5 seconds, that
is 2,000 requests/second of read traffic.

**Fix: Decouple file upload from job creation**

Split `POST /jobs` into two steps:

```
Step 1 — POST /jobs/upload
  Returns: { upload_url: presigned MinIO PUT URL, file_token: uuid }
  Client uploads directly to MinIO — gateway handles zero bytes

Step 2 — POST /jobs
  Body: { file_token, stages }
  Gateway validates file exists in MinIO, creates job, enqueues stages
  No file bytes touch the gateway
```

This reduces `POST /jobs` to a sub-millisecond DB write + Redis push. The gateway
can now handle thousands of job submissions per second because it is not proxying
large file uploads.

**Fix: Horizontal gateway scaling with distributed locks**

```
3 gateway instances behind a load balancer
  + Redis NX lock for watchdog (one leader per cycle)
  + PostgreSQL as shared state (replaces SQLite)
  + Stateless HTTP handlers (no in-process state)
```

#### Bottleneck 2 — SQLite Cannot Support Multiple Writers

At 10,000 jobs/hour with 3 gateway instances, SQLite's single-writer model breaks.
Three gateway instances writing job state simultaneously will serialize on the file lock,
causing request latency spikes on every `POST /jobs`.

**Fix: PostgreSQL with PgBouncer**

```
Gateway instances → PgBouncer (connection pool) → PostgreSQL primary
                                                 → Read replica (for GET /jobs polling)
```

PgBouncer in transaction mode means each gateway instance holds a database connection
only for the duration of a single transaction — not for the lifetime of the HTTP
connection. At 3 gateway instances × 50 concurrent requests each, PgBouncer routes
150 logical connections through 20 actual PostgreSQL connections, staying well within
PostgreSQL's connection limit.

The read replica handles all `GET /jobs/{id}` polling — eventual consistency of a
few hundred milliseconds is acceptable for status polling.

#### Bottleneck 3 — Redis LIST Has No Backpressure

At 10,000 jobs/hour, if workers can't keep up, the Redis LISTs grow without bound.
A single Redis LIST can hold millions of entries, so it won't crash — but unbounded
queue growth means jobs submitted now won't be processed for hours, with no signal
to the submitter.

**Fix: Redis Streams with Consumer Groups + backpressure at submission**

Redis Streams solve two problems simultaneously:

```
Problem 1 — At-most-once delivery (BRPOP destructive read)
  Fix: XREADGROUP + XACK — message stays in stream until acknowledged

Problem 2 — No visibility into pending/processing split
  Fix: Streams have native pending entry list (PEL) — you can see exactly
       how many messages are unacknowledged per consumer
```

Backpressure at submission time:

```python
MAX_QUEUE_DEPTH = int(os.getenv("MAX_QUEUE_DEPTH", 500))

async def submit_job(stage_types):
    for stage_type in stage_types:
        depth = await redis.xlen(f"stream:{stage_type}")
        if depth > MAX_QUEUE_DEPTH:
            return 429, {
                "error": "queue_at_capacity",
                "queue": stage_type,
                "depth": depth,
                "retry_after": 60
            }
```

This turns queue saturation into an explicit client contract (429 Retry-After) rather
than silent multi-hour delays.

#### Bottleneck 4 — Manual Worker Scaling Cannot React Fast Enough

At 10 jobs/hour, `docker-compose up --scale worker-transcode=2` is fine. At 10,000
jobs/hour with bursty traffic patterns (a batch job dumps 500 videos at 9am), manual
scaling means jobs queue up for minutes before a human notices and acts.

**Fix: KEDA on Kubernetes**

KEDA (Kubernetes Event-Driven Autoscaling) reads queue depth directly from Redis
and scales worker Deployments automatically, without any application code changes.

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: worker-transcode-scaler
spec:
  scaleTargetRef:
    name: worker-transcode
  minReplicaCount: 1
  maxReplicaCount: 50
  triggers:
    - type: redis-streams
      metadata:
        address: redis:6379
        stream: stream:transcode
        consumerGroup: transcode-workers
        pendingEntriesCount: "5"     # scale up when > 5 unacked messages per replica
```

KEDA evaluates this every 30 seconds. At 500 queued transcode jobs with `pendingEntriesCount: 5`,
it targets 100 replicas — scaling from 1 to 100 pods in under 2 minutes without any human action.

#### Bottleneck 5 — MinIO Single Instance Has No Redundancy

At 10,000 jobs/hour with parallel uploads and downloads, a single MinIO container
becomes a throughput ceiling. More critically, a single MinIO instance with a single
volume is a total data loss risk.

**Fix: AWS S3 (or MinIO Distributed Mode)**

For production at scale, swap MinIO for S3. The application changes nothing — same
SDK calls, same path conventions. S3 provides:

```
Unlimited throughput  — S3 automatically partitions objects across internal storage nodes
Multi-AZ redundancy   — objects replicated across ≥3 availability zones
Zero operational cost — no volume management, no capacity planning
Lifecycle rules       — automatic tiering and expiry (see Architecture Design doc)
```

If S3 is not an option (on-prem, data residency), MinIO in distributed mode across
4+ nodes provides erasure coding and survives N/2-1 node failures.

---

### Architecture at 10,000 Jobs/Hour

```
                    ┌─────────────────────────────────────┐
                    │         Load Balancer (L7)          │
                    └──────────┬──────────┬───────────────┘
                               │          │
              ┌────────────────▼──┐  ┌────▼───────────────┐
              │   Gateway #1      │  │   Gateway #2        │
              │  (stateless HTTP) │  │  (stateless HTTP)   │
              └────────┬──────────┘  └──────────┬──────────┘
                       │                         │
              ┌────────▼─────────────────────────▼────────┐
              │              PgBouncer                     │
              └────────────────────┬──────────────────────┘
                         ┌─────────┴──────────┐
                   ┌─────▼──────┐     ┌───────▼──────┐
                   │ PostgreSQL │     │  Read Replica │
                   │  Primary   │     │  (polling)    │
                   └────────────┘     └──────────────┘

              ┌─────────────────────────────────────────────┐
              │              Redis Cluster                   │
              │  Streams: stream:overlay / transcode /       │
              │           extract                            │
              │  Presence: worker:presence:*                 │
              │  Locks:    watchdog:leader                   │
              └───────┬─────────────┬───────────────────────┘
                      │             │
    ┌─────────────────▼──┐   ┌──────▼─────────────────────┐
    │  KEDA ScaledObject │   │    KEDA ScaledObject        │
    │  worker-transcode  │   │    worker-overlay           │
    │  1 → 50 replicas   │   │    1 → 50 replicas          │
    └────────────────────┘   └────────────────────────────┘

              ┌─────────────────────────────────────────────┐
              │              AWS S3 / GCS                   │
              │  jobs/{id}/input/   jobs/{id}/stages/{id}/  │
              └─────────────────────────────────────────────┘

              ┌─────────────────────────────────────────────┐
              │          Observability Stack                 │
              │  Prometheus → Grafana (metrics)              │
              │  Loki → Grafana (logs)                       │
              │  Jaeger / Tempo (traces)                     │
              │  PagerDuty (on-call routing)                 │
              └─────────────────────────────────────────────┘
```

---

### Scaling Decision Matrix

| Constraint | Hits at | Solution | Effort |
|---|---|---|---|
| Single gateway upload throughput | ~50 jobs/hr (large files) | Presigned direct-to-S3 uploads | Low |
| SQLite write contention | ~200 jobs/hr (multiple gateways) | PostgreSQL + PgBouncer | Medium |
| Manual worker scaling | ~500 jobs/hr (bursty traffic) | KEDA on Kubernetes | Medium |
| Redis LIST at-most-once | ~1,000 jobs/hr (crash recovery cost) | Redis Streams + Consumer Groups | Medium |
| Single MinIO instance | ~2,000 jobs/hr (I/O throughput) | AWS S3 / MinIO distributed | Low (S3 swap) |
| Single Redis instance | ~5,000 jobs/hr (memory/throughput) | Redis Cluster (3 primary + 3 replica) | High |
| Watchdog leader contention | ~3 gateways | Redis NX distributed lock | Low |
| Gateway CPU (FFmpeg result handling) | ~10,000 jobs/hr | Separate result-processor service | High |

---

### What Does NOT Change at Scale

These design decisions hold from 10 to 10,000 jobs/hour without modification:

**Stateless workers.** A worker at scale is identical to a worker locally — it pulls
from a queue, reads from storage, writes to storage, reports to gateway. Adding 200
more of them changes nothing architecturally.

**Deterministic MinIO output paths as idempotency.** The check `file_exists(output_path)`
before processing works identically whether there is 1 worker or 500. No coordination
required, no distributed locks, no risk of double-processing regardless of scale.

**Retry logic owned by the gateway.** Centralizing retry decisions in the gateway
means there is no distributed consensus problem when deciding whether to re-queue or
dead-letter a stage. At 500 gateway instances this would need revisiting, but at the
scale this system targets (3–10 gateway instances), single-leader watchdog with a
Redis lock is sufficient.

**Per-type worker pools.** Separate queues and worker deployments per job type means
a surge in transcode demand does not starve overlay or extract workers. KEDA scales
each pool independently based on its own queue depth. This isolation is free at any
scale — it is a property of the architecture, not of the deployment size.

---

### Summary

```
Observability gaps to close before production:
  [ ] Structured JSON logging with job_id + stage_id on every line
  [ ] /metrics endpoint with the 8 metric families described above
  [ ] Grafana dashboard covering queue, worker, throughput, and gateway rows
  [ ] 8 alert rules configured with correct severity and for: durations
  [ ] Zombie detection query added to watchdog loop (double-timeout correlation)
  [ ] Worker-vanished detection added to queue monitor loop (presence key expiry check)
  [ ] On-call runbook linked from each alert annotation

Scaling gates:
  [ ] 50+ jobs/hr   → Presigned direct upload, decouple file I/O from gateway
  [ ] 200+ jobs/hr  → PostgreSQL + PgBouncer, horizontal gateway instances
  [ ] 500+ jobs/hr  → Kubernetes + KEDA autoscaling per worker type
  [ ] 1,000+ jobs/hr → Redis Streams + Consumer Groups, explicit backpressure
  [ ] 2,000+ jobs/hr → AWS S3 (swap endpoint), MinIO distributed if on-prem
  [ ] 5,000+ jobs/hr → Redis Cluster, dedicated result-processor service
```


MediaFlow — Agentic Orchestration 
Convert a plain English request into a media processing pipeline.
Implementable in 4–5 hours on top of the existing system.

What This Adds
Instead of calling POST /jobs with a hardcoded stages list, the user types:
"trim this video from 10 to 30 seconds and then burn subtitles on top"
The system figures out the stages, their order, and their parameters. That's it.

The Two Things That Change
1. Schema — two new columns on the stages table
sqlALTER TABLE stages ADD COLUMN depends_on   TEXT;  -- stage_id this stage waits for
ALTER TABLE stages ADD COLUMN parameters   TEXT;  -- JSON string of extra params e.g. {"start":10,"end":30}
ALTER TABLE stages ADD COLUMN input_source TEXT;  -- "job_input" OR "stage:{stage_id}"
Also add a waiting value to the status enum alongside the existing ones.
A stage that has depends_on set starts as waiting instead of pending —
it does not get enqueued until its dependency completes.
2. One new endpoint — POST /agent/submit
Everything else (workers, Redis, MinIO, watchdog) stays exactly the same.

Available Operations — Hardcoded for Now
Four job types. No registry table needed yet.
keywhat it doesextra paramstrimcut video to a time rangestart (seconds), end (seconds)overlayburn subtitles into videosrt_path (MinIO path)transcodedownscale to 480pnoneextractstrip audio to MP3none

The One LLM Call
When POST /agent/submit is hit, make a single call to your LLM with this prompt:
You are a media processing pipeline planner.

Available operations:
- trim: cuts video between start and end seconds. params: {"start": number, "end": number}
- overlay: burns subtitles into video. params: {"srt_path": string}
- transcode: downscales video to 480p. params: {}
- extract: extracts audio to MP3. params: {}

Rules:
- Return a JSON array of stages in execution order.
- Each stage has: type, params, depends_on (null or the type of the previous stage it needs).
- If a stage needs the OUTPUT of a previous stage as its input, set depends_on to that stage's type.
- If stages are independent they can run in parallel (depends_on: null).
- Return ONLY the JSON array. No explanation.

User request: "{user_prompt}"
Uploaded files: {list_of_filenames}

Example output:
[
  { "type": "trim",    "params": {"start": 10, "end": 30}, "depends_on": null },
  { "type": "overlay", "params": {},                        "depends_on": "trim" }
]
The LLM returns a JSON array. You parse it, assign real UUIDs to each stage, resolve
depends_on from type names to actual stage IDs, then insert into the database.

POST /agent/submit — Full Logic
Accept: multipart/form-data
  prompt   — string  e.g. "trim 10 to 30s then burn subtitles"
  file     — video file
  srt_file — optional subtitle file

Steps:

1. Generate job_id (UUID).

2. Upload video to MinIO:  jobs/{job_id}/input/original.{ext}
   If srt_file provided:   jobs/{job_id}/input/subtitles.srt

3. Call LLM with the prompt and filenames → get back JSON array of stages.

4. Assign a UUID to each stage in the array. Build a lookup: type → stage_id.

5. For each stage:
     - If depends_on is a type name, replace it with the actual stage_id from the lookup.
     - Set input_source:
         depends_on is null   → "job_input"
         depends_on is set    → "stage:{depends_on_stage_id}"
     - If type is "overlay" and srt_file was uploaded:
         set params.srt_path = "jobs/{job_id}/input/subtitles.srt"
     - Set status:
         depends_on is null   → "pending"   (enqueue immediately)
         depends_on is set    → "waiting"   (do not enqueue yet)

6. INSERT job record. INSERT all stage records.

7. ENQUEUE only the stages with status = "pending".

8. Return 202 with { job_id, pipeline: [stages] }

Dependency Resolution — One Small Gateway Change
In the existing PATCH /internal/stages/{stage_id} handler, add this block
after marking a stage as completed:
When status = "completed":
  UPDATE stage → completed, output_path = body.output_path

  NEW: check for waiting dependents
  SELECT * FROM stages WHERE depends_on = {stage_id} AND status = "waiting"
  For each result:
    UPDATE stage SET status = "pending", input_source = "stage:{stage_id}"
    ENQUEUE stage → queue:{type}
That's the entire dependency engine. When trim finishes, it automatically promotes
and enqueues the overlay stage that was waiting on it.

Worker Change — Read input_source
The worker already fetches its input from MinIO. Change the input fetch logic to
read input_source from the queue message instead of always using job_input:
input_source = message.get("input_source", "job_input")

if input_source == "job_input":
    path = f"jobs/{job_id}/input/original.{ext}"
else:
    # input_source = "stage:{stage_id}"
    source_stage_id = input_source.split(":")[1]
    path = f"jobs/{job_id}/stages/{source_stage_id}/output.{ext}"

input_bytes = minio.download(path)
Pass input_source in the queue message when enqueuing so the worker has it.

Trim Processor — New Worker
You need one new processor since trim doesn't exist yet.
workers/trim/processor.py

FFmpeg command:
  ffmpeg -y -i {input} -ss {start} -to {end} -c copy {output}

  -ss {start}   start time in seconds
  -to {end}     end time in seconds
  -c copy       copy streams without re-encoding (very fast)

Timeout: 5 minutes
Output extension: same as input
Read start and end from message["params"]["start"] and message["params"]["end"]
Add worker-trim to docker-compose with JOB_TYPE=trim.

What a Request Looks Like End-to-End
User: "trim 10 to 30 seconds then burn subtitles"

POST /agent/submit
  prompt = "trim 10 to 30 seconds then burn subtitles"
  file   = video.mp4
  srt    = subtitles.srt

LLM returns:
  [
    { "type": "trim",    "params": {"start":10, "end":30}, "depends_on": null },
    { "type": "overlay", "params": {},                      "depends_on": "trim" }
  ]

Gateway inserts:
  stage a1b2  type=trim     status=pending  depends_on=null        input_source=job_input
  stage c3d4  type=overlay  status=waiting  depends_on=a1b2        input_source=stage:a1b2

Gateway enqueues:  queue:trim ← a1b2  (only this one)

Trim worker runs on original video → uploads trimmed output → reports completed

Gateway promotes c3d4:
  status=pending, input_source=stage:a1b2
  ENQUEUE queue:overlay ← c3d4

Overlay worker runs on TRIM output (not original) → burns subtitles → done

GET /jobs/{job_id}
  → trim: completed, overlay: completed
  → presigned URL for final output
