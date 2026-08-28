package accounttesttask

// Schema contains only the lease relation owned by this package. The
// account_test_tasks relation itself is created by the Business maintenance
// migration and is intentionally not created at runtime.
const Schema = `CREATE TABLE IF NOT EXISTS account_test_task_leases (
  task_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  fence_token INTEGER NOT NULL CHECK (fence_token >= 1),
  lease_until TEXT NOT NULL,
  updated_at TEXT NOT NULL
);`
