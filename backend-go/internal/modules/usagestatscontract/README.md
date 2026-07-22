# Usage Stats Aggregation Contract

This package is a deterministic contract for the future Go usage-stats writer. It
does not open PostgreSQL, register a worker, write derived tables, or change the
current Node single-writer owner.

The contract locks down five behaviors:

- source rows are ordered by `(created_at, id)`, are strictly after the persisted
  cursor, and are no newer than the safety fence;
- one shard scan visits at most 16 shards and advances as a ring, so a large shard
  registry cannot cause unbounded fan-out;
- hot window publication uses the configured statistics timezone and a fixed
  31-day horizon;
- a writer lease uses a monotonically increasing fencing token and canonical UTC
  RFC3339 source watermark. A future writer must persist derived rows and the
  returned checkpoint in the same PostgreSQL transaction, with a conditional
  fence update that rejects stale holders or a watermark regression;
- account access snapshots only move `last_used_at` forward. Equal timestamps use
  the source cursor as a deterministic tie-breaker.

The future storage implementation must keep aggregation cursor advancement,
derived aggregation, access snapshots, and publication checkpoint atomic. A
fencing rejection is not a retryable data-write error: the worker must stop the
stale lease holder and acquire a new lease before continuing.

Run the executable golden contract with:

```powershell
go test ./internal/modules/usagestatscontract -count=1
```
