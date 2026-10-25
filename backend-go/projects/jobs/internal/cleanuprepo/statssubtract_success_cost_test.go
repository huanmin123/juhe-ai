package cleanuprepo

import (
	"context"
	"fmt"
	"testing"

)

func readKitScopeCostTuple(t *testing.T, stats *DB, table, timeColumn string, timeValue any) [4]float64 {
	t.Helper()
	query := fmt.Sprintf(`SELECT request_count, error_count, total_cost_usd, success_cost_usd FROM %s
		WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`, table)
	args := []any{}
	if timeColumn != "" {
		query += " AND " + timeColumn + " = ?"
		args = append(args, timeValue)
	}
	rows, err := stats.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("read %s tuple: %v", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("%s 元组行不存在（被误删或未命中）", table)
	}
	var request, errors, total, success float64
	if err := rows.Scan(&request, &errors, &total, &success); err != nil {
		t.Fatalf("scan %s tuple: %v", table, err)
	}
	return [4]float64{request, errors, total, success}
}

