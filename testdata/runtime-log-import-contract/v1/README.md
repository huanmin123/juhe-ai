# Runtime log import contract v1

This directory freezes the migration contract for the Node-owned runtime log file importer at commit `0bcea242ddba0a6ed1bca064a9ff5c528198d992`.

It is a read-only golden. Tests must never rewrite it. A later Node business change requires a reviewed `v2` fixture or an explicit amendment; test execution must not fetch, generate, or update expected data.

## Ownership boundary

- Node remains the production file importer, cursor writer, facet writer, rotation-protection owner, and retention scheduler.
- This fixture changes no schema, deployment switch, writer route, or production owner.
- Go may consume the contract in later implementation slices, but owner cutover requires a separate deploy and rollback decision.

## Business principles to preserve

- Discover only controlled role files, with bounded continuation and rotated files before current files.
- Track physical identity, byte offset, line number, and truncation generation. A moved identity keeps its cursor; reused offsets after truncation receive a new generation.
- Index only newline-terminated records. Invalid complete records remain diagnosable and retain source coordinates.
- Normalize stored timestamps to canonical UTC milliseconds. Deterministic source IDs make replay idempotent for rows and facets.
- A failed batch cannot advance the durable cursor. Pending or failed rotated files remain protected from file cleanup.
- Row retention and facet subtraction are one consistency boundary. Completed, error-free old cursors may be removed independently.
- `JUHE_AI_RUNTIME_LOG_INDEX_ENABLED=false` disables index work but keeps file logging and grep, preserves historical index data, and releases normal rotated-file cleanup.

## Confirmed Node defects not to copy

1. The W6 migration record says `raw_json` is limited to 128 KiB, while the current repository stores it unchanged and the Node large-line regression requires more than 1 MiB to remain intact. Go must enforce a UTF-8-safe 131072-byte representation with an in-budget truncation marker.
2. Node commits rows/facets and cursor checkpoints in separate database operations. Stable IDs make replay mostly idempotent, but a crash window remains. Go should commit rows, facet deltas, and the cursor checkpoint in one PostgreSQL transaction.

## Go-native implementation direction

- Use a bounded `bufio.Reader` design with explicit line/byte budgets and cancellation.
- Protect the single writer with deployment ownership plus a PostgreSQL advisory lock or equivalent lease.
- Use deterministic IDs and insert-only facet deltas inside the batch transaction.
- Use bounded ordered retention claims; use `FOR UPDATE SKIP LOCKED` when cleanup can overlap.

The ordered implementation slices are recorded in `contract.json`. They deliberately separate parser/reader, transactional storage, discovery/rotation worker, and retention/cutover so each can be migrated and reviewed without a second runtime owner.
