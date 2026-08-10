# Audit Writer Golden Contract

`audit-writer-contract-v1.json` freezes the Node reference behavior for the future Go audit writer.

It is intentionally a contract-only asset. It does not introduce a Go queue, transport, persistence writer, or route owner. The `test:audit-writer-contract-golden` command validates the fixture and executes every listed Node reference regression. A Go implementation must add an independent consumer of this fixture before it becomes an owner.
