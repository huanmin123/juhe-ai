// Tests for the AI 账户特供快速恢复通道 (expedited recovery) schema changes
// (设计 §7 存储与 schema 变更、§11.8 验证要求)：
//   - PG：建表 DDL、既有库 ALTER 守卫与 new_accounts / old_accounts 触发器
//     ROW 投影的成对同源断言（live PostgreSQL 走 opt-in smoke，这里照
//     TestPostgresQualityScheduleIntervalCheckContract 的形态钉 SQL 文本契约）。
//   - SQLite：fresh DDL 含列且 CHECK 生效；既有库无列 → 加列；已有列 → 早退幂等。
//   - contracts：两列已登记进 BusinessSQLiteSchema 契约清单。

package schema

import (
	"regexp"
	"strings"
	"testing"
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

	// BUG-0305 发布事故守卫：accounts.expedited_recovery_enabled 的存量库
	// ALTER 必须在位——CREATE TABLE 只救新库，缺它时存量 PG 库新二进制启动
	// 契约校验缺列 fail-closed 崩溃循环。
	accountAlterFound := false
	for _, statement := range postgresSchemaStatements {
		if statement.Source != "account-expedited-recovery-pg-column" {
			continue
		}
		accountAlterFound = true
		want := `ALTER TABLE accounts ADD COLUMN IF NOT EXISTS expedited_recovery_enabled integer NOT NULL DEFAULT 0 CHECK (expedited_recovery_enabled IN (0, 1))`
		if statement.SQL != want {
			t.Fatalf("accounts legacy ALTER guard drifted:\n got %q\nwant %q", statement.SQL, want)
		}
	}
	if !accountAlterFound {
		t.Fatal("account-expedited-recovery-pg-column ALTER guard statement missing")
	}

	triggerSQL := findPostgresStatementContaining(t, "CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()")
	fnStart := strings.Index(triggerSQL, "CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()")
	body := triggerSQL[fnStart:]
	rowStart := strings.Index(body, "WHERE ROW(")
	rowMid := strings.Index(body, ") IS DISTINCT FROM ROW(")
	if rowStart < 0 || rowMid < 0 || rowMid < rowStart {
		t.Fatalf("update dirty trigger ROW comparison not found:\n%.400s", body)
	}
	newSection := body[rowStart+len("WHERE ROW(") : rowMid]
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
