package taskruns

// sqliteSchema 与 Node backend/src/storage/schema/stats-schema.ts 中
// background_task_runs / background_job_leases 冻结 DDL 一致（含索引）。
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS background_task_runs (
  run_id TEXT PRIMARY KEY,
  job_name TEXT NOT NULL,
  job_type TEXT NOT NULL,
  worker_role TEXT NOT NULL,
  status TEXT NOT NULL,
  lease_key TEXT NOT NULL,
  owner_id TEXT,
  params_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}',
  error_message TEXT,
  submitted_at TEXT NOT NULL,
  started_at TEXT,
  heartbeat_at TEXT,
  finished_at TEXT,
  duration_ms INTEGER,
  exit_code INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS background_job_leases (
  lease_key TEXT PRIMARY KEY,
  job_name TEXT NOT NULL,
  shard_key TEXT NOT NULL DEFAULT '',
  owner_id TEXT NOT NULL,
  run_id TEXT,
  fencing_token INTEGER NOT NULL DEFAULT 0,
  lease_until TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_background_task_runs_status_updated
  ON background_task_runs(status, updated_at DESC, run_id DESC);

CREATE INDEX IF NOT EXISTS idx_background_task_runs_job_created
  ON background_task_runs(job_name, created_at DESC, run_id DESC);

CREATE INDEX IF NOT EXISTS idx_background_job_leases_job
  ON background_job_leases(job_name, shard_key, lease_until);
`

// postgresSchema 与 backend-go/projects/maintenance/internal/schema/pg_schema.go
// 的 juhe_stats 冻结 DDL 一致。
const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_stats.background_task_runs (
  run_id text PRIMARY KEY,
  job_name text NOT NULL,
  job_type text NOT NULL,
  worker_role text NOT NULL,
  status text NOT NULL,
  lease_key text NOT NULL,
  owner_id text,
  params_json text NOT NULL DEFAULT '{}',
  result_json text NOT NULL DEFAULT '{}',
  error_message text,
  submitted_at text NOT NULL,
  started_at text,
  heartbeat_at text,
  finished_at text,
  duration_ms integer,
  exit_code integer,
  created_at text NOT NULL,
  updated_at text NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_stats.background_job_leases (
  lease_key text PRIMARY KEY,
  job_name text NOT NULL,
  shard_key text NOT NULL DEFAULT '',
  owner_id text NOT NULL,
  run_id text,
  fencing_token bigint NOT NULL DEFAULT 0,
  lease_until text NOT NULL,
  heartbeat_at text NOT NULL,
  started_at text NOT NULL,
  updated_at text NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_background_task_runs_status_updated
  ON juhe_stats.background_task_runs(status, updated_at DESC, run_id DESC);

CREATE INDEX IF NOT EXISTS idx_background_task_runs_job_created
  ON juhe_stats.background_task_runs(job_name, created_at DESC, run_id DESC);

CREATE INDEX IF NOT EXISTS idx_background_job_leases_job
  ON juhe_stats.background_job_leases(job_name, shard_key, lease_until);
`
