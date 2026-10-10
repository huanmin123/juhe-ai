// Tests for the AI 账户特供快速恢复通道 (expedited recovery) schema changes
// (设计 §7 存储与 schema 变更、§11.8 验证要求)：
//   - PG：建表 DDL、既有库 ALTER 守卫与 new_accounts / old_accounts 触发器
//     ROW 投影的成对同源断言（live PostgreSQL 走 opt-in smoke，这里照
//     TestPostgresQualityScheduleIntervalCheckContract 的形态钉 SQL 文本契约）。
//   - SQLite：fresh DDL 含列且 CHECK 生效；既有库无列 → 加列；已有列 → 早退幂等。
//   - contracts：两列已登记进 BusinessSQLiteSchema 契约清单。

package schema

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// findPostgresStatementContaining returns the first schema statement whose SQL
// contains needle, failing the test when none matches.
func findPostgresStatementContaining(t *testing.T, needle string) string {
	t.Helper()
	for _, statement := range postgresSchemaStatements {
		if strings.Contains(statement.SQL, needle) {
			return statement.SQL
		}
	}
	t.Fatalf("postgres schema statement containing %q not found", needle)
	return ""
}

// pgColumnDeclarationLine extracts the single DDL line declaring column inside
// tableDDL (used to pin nullability/defaults on the exact line).
func pgColumnDeclarationLine(t *testing.T, tableDDL, column string) string {
	t.Helper()
	for _, line := range strings.Split(tableDDL, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), column+" ") {
			return line
		}
	}
	t.Fatalf("column %q declaration line not found in table DDL:\n%s", column, tableDDL)
	return ""
}

// TestPostgresExpeditedRecoverySchemaContract pins the expedited-recovery PG
// schema contract: accounts.expedited_recovery_enabled in the CREATE TABLE
// DDL next to temporary_unavailable_continuous_probe_enabled, the paired
// new_accounts / old_accounts trigger ROW projections (missing either side
// would drop the column from change detection), the nullable
// system_accounts.expedited_account_limit and its guarded legacy ALTER.
func TestPostgresExpeditedRecoverySchemaContract(t *testing.T) {
	accountsDDL := findPostgresStatementContaining(t, "CREATE TABLE IF NOT EXISTS accounts (")
	expeditedLine := pgColumnDeclarationLine(t, accountsDDL, "expedited_recovery_enabled")
	if expeditedLine != "      expedited_recovery_enabled integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1))," {
		t.Fatalf("accounts.expedited_recovery_enabled declaration drifted: %q", expeditedLine)
	}
	probeIndex := strings.Index(accountsDDL, "temporary_unavailable_continuous_probe_enabled integer NOT NULL")
	expeditedIndex := strings.Index(accountsDDL, "expedited_recovery_enabled integer")
	if probeIndex < 0 || expeditedIndex < probeIndex {
		t.Fatalf("accounts.expedited_recovery_enabled must sit next to temporary_unavailable_continuous_probe_enabled (probe at %d, expedited at %d)", probeIndex, expeditedIndex)
	}

	systemAccountsDDL := findPostgresStatementContaining(t, "CREATE TABLE IF NOT EXISTS system_accounts (")
	limitLine := pgColumnDeclarationLine(t, systemAccountsDDL, "expedited_account_limit")
	if limitLine != "      expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100)," {
		t.Fatalf("system_accounts.expedited_account_limit declaration drifted: %q", limitLine)
	}
	// 可空、无默认值：NULL=默认 3 的语义在读取侧 COALESCE，不写 DB 默认。
	if strings.Contains(limitLine, "NOT NULL") || strings.Contains(limitLine, "DEFAULT") {
		t.Fatalf("system_accounts.expedited_account_limit must stay nullable without a DB default: %q", limitLine)
	}

	alterFound := false
	for _, statement := range postgresSchemaStatements {
		if statement.Source != "system-account-expedited-account-limit-pg-column" {
			continue
		}
		alterFound = true
		want := `ALTER TABLE system_accounts ADD COLUMN IF NOT EXISTS expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100)`
		if statement.SQL != want {
			t.Fatalf("legacy ALTER guard drifted:\n got %q\nwant %q", statement.SQL, want)
		}
	}
	if !alterFound {
		t.Fatal("system-account-expedited-account-limit-pg-column ALTER guard statement missing")
	}

	triggerSQL := findPostgresStatementContaining(t, "CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()")
	fnStart := strings.Index(triggerSQL, "CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()")
	body := triggerSQL[fnStart:]
	rowStart := strings.Index(body, "WHERE ROW(")
	rowMid := strings.Index(body, ") IS DISTINCT FROM ROW(")
	if rowStart < 0 || rowMid < 0 || rowMid < rowStart {
		t.Fatalf("update dirty trigger ROW comparison not found:\n%.400s", body)
	}
	newSection := body[rowStart+len("WHERE ROW("):rowMid]
	oldRest := body[rowMid+len(") IS DISTINCT FROM ROW("):]
	oldEnd := strings.Index(oldRest, ");")
	if oldEnd < 0 {
		t.Fatalf("old_accounts ROW projection terminator not found:\n%.400s", oldRest)
	}
	oldSection := oldRest[:oldEnd]

	newColumns := regexp.MustCompile(`new_accounts\.(\w+)`).FindAllStringSubmatch(newSection, -1)
	oldColumns := regexp.MustCompile(`old_accounts\.(\w+)`).FindAllStringSubmatch(oldSection, -1)
	if len(newColumns) == 0 || len(newColumns) != len(oldColumns) {
		t.Fatalf("trigger ROW projections must be paired: new_accounts has %d entries, old_accounts has %d", len(newColumns), len(oldColumns))
	}
	newNames := make([]string, 0, len(newColumns))
	oldNames := make([]string, 0, len(oldColumns))
	for _, match := range newColumns {
		newNames = append(newNames, match[1])
	}
	for _, match := range oldColumns {
		oldNames = append(oldNames, match[1])
	}
	for i := range newNames {
		if newNames[i] != oldNames[i] {
			t.Fatalf("trigger ROW projections diverged at position %d: new_accounts.%s vs old_accounts.%s", i, newNames[i], oldNames[i])
		}
	}
	for _, side := range []struct {
		name    string
		columns []string
	}{
		{"new_accounts", newNames},
		{"old_accounts", oldNames},
	} {
		count := 0
		position := -1
		probePosition := -1
		for i, name := range side.columns {
			switch name {
			case "expedited_recovery_enabled":
				count++
				position = i
			case "temporary_unavailable_continuous_probe_enabled":
				probePosition = i
			}
		}
		if count != 1 {
			t.Fatalf("%s ROW projection must list expedited_recovery_enabled exactly once, got %d", side.name, count)
		}
		if probePosition < 0 || position != probePosition+1 {
			t.Fatalf("%s ROW projection must place expedited_recovery_enabled right after temporary_unavailable_continuous_probe_enabled (probe at %d, expedited at %d)", side.name, probePosition, position)
		}
	}
}

// sqliteColumnMeta reads notnull/dflt_value for one column via PRAGMA
// table_info; found reports whether the column exists.
func sqliteColumnMeta(t *testing.T, db *sql.DB, table, column string) (notnull int, dflt sql.NullString, found bool) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma table_info(%s): %v", table, err)
		}
		if name != column {
			continue
		}
		if defaultValue == nil {
			return notNull, sql.NullString{}, true
		}
		value, ok := defaultValue.(string)
		if !ok {
			t.Fatalf("pragma table_info(%s) dflt_value for %s has unexpected type %T", table, column, defaultValue)
		}
		return notNull, sql.NullString{String: value, Valid: true}, true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma table_info(%s): %v", table, err)
	}
	return 0, sql.NullString{}, false
}

// sqliteColumnNames returns the ordered column names of a table.
func sqliteColumnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma table_info(%s): %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma table_info(%s): %v", table, err)
	}
	return names
}

// legacyBusinessDDLForExpeditedColumns rebuilds the pre-feature business DDL by
// stripping the two new column declarations from the current main DDL, so the
// legacy fixture stays byte-faithful to the shipped schema minus the new
// columns. The replaced lines leave blank lines, which are inert SQL.
func legacyBusinessDDLForExpeditedColumns(t *testing.T) string {
	t.Helper()
	legacyDDL := strings.NewReplacer(
		"      expedited_account_limit INTEGER CHECK (expedited_account_limit BETWEEN 0 AND 100),", "",
		"      expedited_recovery_enabled INTEGER NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1)),", "",
	).Replace(sqliteBusinessMainDDL)
	if strings.Contains(legacyDDL, "expedited_recovery_enabled") || strings.Contains(legacyDDL, "expedited_account_limit") {
		t.Fatal("legacy fixture still declares the expedited columns")
	}
	return legacyDDL
}

// insertMinimalAccountLegacy seeds one accounts row on a database that predates
// the expedited columns (no parent rows required; callers keep foreign keys off
// or rely on the connection default).
func insertMinimalAccountLegacy(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO accounts
      (id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
       name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at)
      VALUES (?, 'sys-1', 'gpt', 'profile-1', 'openai', 'v1', ?, 'api_key', 'enc', 'gpt-5.6', 'chat_json',
       '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, id, id)
	if err != nil {
		t.Fatalf("insert legacy account %s: %v", id, err)
	}
}

// insertMinimalSystemAccount seeds one system_accounts row with an explicit
// expedited_account_limit (pass nil for NULL = 读取侧默认 3).
func insertMinimalSystemAccount(t *testing.T, db *sql.DB, username string, expeditedLimit any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO system_accounts
      (username, display_name, password_hash, created_at, updated_at, expedited_account_limit)
      VALUES (?, ?, 'hash', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', ?)`, username, username, expeditedLimit)
	if err != nil {
		t.Fatalf("insert system account %s: %v", username, err)
	}
}

// TestEnsureSQLiteBusinessAddsExpeditedRecoveryColumns covers both expedited
// columns end to end: fresh databases declare them inside the CREATE TABLE
// statements (with the column CHECKs enforced), legacy databases receive them
// through the guarded PRAGMA table_info / ALTER TABLE ADD COLUMN migration with
// expedited_recovery_enabled defaulting to 0 and expedited_account_limit left
// NULL (读取侧 COALESCE 默认 3)， and repeated ensure runs exit early instead of
// re-ALTERing.
func TestEnsureSQLiteBusinessAddsExpeditedRecoveryColumns(t *testing.T) {
	legacyPrelude := func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := db.Exec(legacyBusinessDDLForExpeditedColumns(t)); err != nil {
			t.Fatalf("seed legacy business DDL: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO system_accounts (username, display_name, password_hash, created_at, updated_at)
			VALUES ('legacy-user', 'legacy', 'hash', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`); err != nil {
			t.Fatalf("seed legacy system account: %v", err)
		}
		// 连接默认关闭外键，无需父行即可播种存量 accounts 行。
		insertMinimalAccountLegacy(t, db, "acct-legacy")
	}

	t.Run("fresh database declares the columns with working checks", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-expedited-fresh")
		ctx := context.Background()
		if _, err := EnsureSQLiteBusiness(ctx, db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness: %v", err)
		}
		if !sqliteTableHasColumn(t, db, "accounts", "expedited_recovery_enabled") {
			t.Error("fresh accounts lacks expedited_recovery_enabled")
		}
		if !sqliteTableHasColumn(t, db, "system_accounts", "expedited_account_limit") {
			t.Error("fresh system_accounts lacks expedited_account_limit")
		}
		notNull, dflt, _ := sqliteColumnMeta(t, db, "accounts", "expedited_recovery_enabled")
		if notNull != 1 || !dflt.Valid || dflt.String != "0" {
			t.Fatalf("accounts.expedited_recovery_enabled meta: notnull=%d dflt=%+v, want notnull=1 dflt='0'", notNull, dflt)
		}
		notNull, dflt, _ = sqliteColumnMeta(t, db, "system_accounts", "expedited_account_limit")
		if notNull != 0 || dflt.Valid {
			t.Fatalf("system_accounts.expedited_account_limit meta: notnull=%d dflt=%+v, want nullable without default", notNull, dflt)
		}

		// 契约清单声明的列必须全部由 fresh DDL 落地（两表）。
		for _, target := range []struct {
			table    string
			contract contracts.SQLiteTableSpec
		}{
			{"accounts", contracts.BusinessSQLiteSchema["accounts"]},
			{"system_accounts", contracts.BusinessSQLiteSchema["system_accounts"]},
		} {
			for _, column := range target.contract.Columns {
				if !sqliteTableHasColumn(t, db, target.table, column) {
					t.Errorf("contract column %s.%s missing from fresh schema", target.table, column)
				}
			}
		}

		// CHECK 生效：插入前关闭外键，保证拒绝可归因于列级 CHECK 而非外键。
		if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatalf("disable foreign_keys: %v", err)
		}
		insertMinimalAccountLegacy(t, db, "acct-ok-flag-1")
		if _, err := db.Exec(`UPDATE accounts SET expedited_recovery_enabled = 1 WHERE id = 'acct-ok-flag-1'`); err != nil {
			t.Fatalf("expedited_recovery_enabled=1 must be accepted: %v", err)
		}
		if _, err := db.Exec(`UPDATE accounts SET expedited_recovery_enabled = 2 WHERE id = 'acct-ok-flag-1'`); err == nil {
			t.Fatal("expedited_recovery_enabled=2 must be rejected by the column CHECK")
		}
		insertMinimalSystemAccount(t, db, "limit-ok", 100)
		insertMinimalSystemAccount(t, db, "limit-null", nil)
		if _, err := db.Exec(`INSERT INTO system_accounts
      (username, display_name, password_hash, created_at, updated_at, expedited_account_limit)
      VALUES ('limit-over', 'over', 'hash', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', 101)`); err == nil {
			t.Fatal("expedited_account_limit=101 must be rejected by the column CHECK")
		}
		// NULL 名额合法（NULL=读取侧默认 3）。
		var nullLimit sql.NullInt64
		if err := db.QueryRow(`SELECT expedited_account_limit FROM system_accounts WHERE username = 'limit-null'`).Scan(&nullLimit); err != nil {
			t.Fatalf("read null limit: %v", err)
		}
		if nullLimit.Valid {
			t.Fatalf("expedited_account_limit = %d, want NULL", nullLimit.Int64)
		}
	})

	t.Run("legacy database receives the guarded ALTER", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-expedited-legacy")
		legacyPrelude(t, db)
		if sqliteTableHasColumn(t, db, "accounts", "expedited_recovery_enabled") {
			t.Fatal("legacy precondition violated: accounts already has expedited_recovery_enabled")
		}
		if sqliteTableHasColumn(t, db, "system_accounts", "expedited_account_limit") {
			t.Fatal("legacy precondition violated: system_accounts already has expedited_account_limit")
		}
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("EnsureSQLiteBusiness over legacy tables: %v", err)
		}
		if !sqliteTableHasColumn(t, db, "accounts", "expedited_recovery_enabled") {
			t.Error("legacy accounts lacks expedited_recovery_enabled after ensure")
		}
		if !sqliteTableHasColumn(t, db, "system_accounts", "expedited_account_limit") {
			t.Error("legacy system_accounts lacks expedited_account_limit after ensure")
		}
		// 存量行：恢复道默认 0；名额列在 system_accounts 上保持 NULL
		//（读取侧归一为默认 3）。
		var expedited int
		if err := db.QueryRow(`SELECT expedited_recovery_enabled FROM accounts WHERE id = 'acct-legacy'`).Scan(&expedited); err != nil {
			t.Fatalf("read legacy account row: %v", err)
		}
		if expedited != 0 {
			t.Fatalf("legacy row expedited_recovery_enabled = %d, want default 0", expedited)
		}
		var limit sql.NullInt64
		if err := db.QueryRow(`SELECT expedited_account_limit FROM system_accounts WHERE username = 'legacy-user'`).Scan(&limit); err != nil {
			t.Fatalf("read legacy system account row: %v", err)
		}
		if limit.Valid {
			t.Fatalf("legacy row expedited_account_limit = %d, want NULL", limit.Int64)
		}
	})

	t.Run("repeated ensure exits early when the columns already exist", func(t *testing.T) {
		db := openSharedMemorySQLite(t, "authsys-schema-test-expedited-idempotent")
		legacyPrelude(t, db)
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("first EnsureSQLiteBusiness: %v", err)
		}
		accountsColumns := sqliteColumnNames(t, db, "accounts")
		systemAccountsColumns := sqliteColumnNames(t, db, "system_accounts")

		// SQLite 重复 ADD COLUMN 会报 duplicate column name：第二次 ensure
		// 成功即证明守卫按 PRAGMA table_info 早退，未重复加列。
		if _, err := EnsureSQLiteBusiness(context.Background(), db); err != nil {
			t.Fatalf("second EnsureSQLiteBusiness must exit the guards early: %v", err)
		}
		if got := sqliteColumnNames(t, db, "accounts"); strings.Join(got, ",") != strings.Join(accountsColumns, ",") {
			t.Fatalf("accounts column set changed on rerun:\n got %v\nwant %v", got, accountsColumns)
		}
		if got := sqliteColumnNames(t, db, "system_accounts"); strings.Join(got, ",") != strings.Join(systemAccountsColumns, ",") {
			t.Fatalf("system_accounts column set changed on rerun:\n got %v\nwant %v", got, systemAccountsColumns)
		}
	})
}

// TestBusinessSQLiteContractRegistersExpeditedColumns guards the contract
// registration side (设计 §7.3)： both columns must stay listed in the
// Business SQLite schema contract next to their anchor columns, so the Gateway
// schemaReady gate keeps covering them.
func TestBusinessSQLiteContractRegistersExpeditedColumns(t *testing.T) {
	positionOf := func(columns []string, column string) int {
		for i, name := range columns {
			if name == column {
				return i
			}
		}
		return -1
	}
	accounts := contracts.BusinessSQLiteSchema["accounts"].Columns
	expedited := positionOf(accounts, "expedited_recovery_enabled")
	probe := positionOf(accounts, "temporary_unavailable_continuous_probe_enabled")
	if probe < 0 || expedited != probe+1 {
		t.Fatalf("contract accounts columns must list expedited_recovery_enabled right after temporary_unavailable_continuous_probe_enabled (probe at %d, expedited at %d)", probe, expedited)
	}
	if positionOf(contracts.BusinessSQLiteSchema["system_accounts"].Columns, "expedited_account_limit") < 0 {
		t.Fatal("contract system_accounts columns must list expedited_account_limit")
	}
}
