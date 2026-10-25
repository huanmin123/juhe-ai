package circuitstore

import (
	"database/sql"
	"testing"

)

func seedIncident(t *testing.T, db *sql.DB, scopeKey, accountID, runtimeKey, state string, generation, dispatchRevision, ledgerRevision, updatedAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind,
			incident_id, child_incident_ids_json, state,
			generation, dispatch_revision, ledger_revision, transition_id,
			created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, 'account', ?, '[]', ?, ?, ?, ?, 'tr-1', ?, ?)`,
		scopeKey, accountID, runtimeKey, "incident-"+scopeKey, state,
		generation, dispatchRevision, ledgerRevision, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
}

// kitSeedIncident 是 seedIncident 的 PG fixture 变体（juhe_business schema 限定
// 与 $n 占位符；断言语义与失败消息逐字节一致）。w13g5 注入臂的 seedIncident
// 保持 SQLite 原状（scripted 臂原样保留）。
func kitSeedIncident(t *testing.T, db *sql.DB, scopeKey, accountID, runtimeKey, state string, generation, dispatchRevision, ledgerRevision, updatedAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO juhe_business.account_circuit_incidents (
			circuit_scope_key, account_id, account_runtime_key, scope_kind,
			incident_id, child_incident_ids_json, state,
			generation, dispatch_revision, ledger_revision, transition_id,
			created_at_ms, updated_at_ms
		) VALUES ($1, $2, $3, 'account', $4, '[]', $5, $6, $7, $8, 'tr-1', $9, $9)`,
		scopeKey, accountID, runtimeKey, "incident-"+scopeKey, state,
		generation, dispatchRevision, ledgerRevision, updatedAt); err != nil {
		t.Fatal(err)
	}
}

