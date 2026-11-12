package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEpochInstantColumnsAreTimestamptz 钉住 Phase 5 运行态瞬时列的目标类型：
// 这些列参与租约/CAS 比较，禁止回退到 epoch 毫秒 bigint。
func TestEpochInstantColumnsAreTimestamptz(t *testing.T) {
	raw, err := schemaSource("pg_schema_business_tables.go")
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{
		"next_retry_at_ms", "open_until_ms", "next_transition_at_ms",
		"lease_until_ms", "attempt_started_at_ms", "attempt_hard_deadline_ms",
		"retained_until_ms", "created_at_ms", "updated_at_ms",
		"available_at_ms", "claim_until_ms", "acknowledged_at_ms",
	}
	for _, column := range columns {
		if strings.Contains(raw, column+" bigint") {
			t.Fatalf("%s 仍是 epoch 毫秒 bigint", column)
		}
		if !strings.Contains(raw, column+" timestamptz(6)") {
			t.Fatalf("%s 未钉住为 timestamptz(6)", column)
		}
	}
}

func schemaSource(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join("pg_schema_business_tables.go"))
	return string(data), err
}
