package j3bmodelcheck

// w12g 波次：backfillPostgresTable 与 backfillPostgresTrustAggregationState
// 的脚本化分支覆盖（上限越界、冲突、检查/插入/prepare 失败、迭代/关闭/计数错误）。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestW12GBackfillPostgresTableScripted(t *testing.T) {
	ctx := context.Background()
	columnsRows := func() w12gFakeQuery {
		return w12gFakeQuery{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"id", "text", "text", "NO", nil, "NO"},
			{"score", "integer", "int4", "YES", nil, "NO"},
		}}}
	}
	exists := func() w12gFakeQuery {
		return w12gFakeQuery{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}}
	}
	pks := func() w12gFakeQuery {
		return w12gFakeQuery{match: "pg_index", rows: &w12gFakeRows{columns: []string{"attname"}, rows: [][]driver.Value{{"id"}}}}
	}
	sourceRows := func(values ...driver.Value) w12gFakeQuery {
		return w12gFakeQuery{match: `FROM "juhe_dataset"."model_check_runs"`, rows: &w12gFakeRows{columns: []string{"id", "score"}, rows: [][]driver.Value{values}}}
	}

	build := func(extra ...w12gFakeQuery) *w12gFakeConn {
		queries := []w12gFakeQuery{exists(), exists(), columnsRows(), columnsRows(), pks(), pks()}
		return &w12gFakeConn{queries: append(queries, extra...)}
	}

	item := postgresBackfillTable{name: "model_check_runs", sourceSchema: "juhe_dataset"}
	options := normalizedPostgresBackfillOptions{maxRows: 10, maxBytes: 1 << 20}
	targetEmpty := w12gFakeQuery{match: `FROM "juhe_j3b"."model_check_runs" WHERE`, rows: &w12gFakeRows{columns: []string{"id", "score"}}}

	t.Run("happy path insert", func(t *testing.T) {
		conn := build(sourceRows("r1", int64(7)), targetEmpty)
		_, tx := w12gBeginTx(t, conn)
		result, err := backfillPostgresTable(ctx, tx, item, options)
		if err != nil {
			t.Fatal(err)
		}
		if result.SourceRows != 1 || result.InsertedRows != 1 || len(result.Projection) != 2 {
			t.Fatalf("单行插入: %+v", result)
		}
	})

	t.Run("source row exceed max rows", func(t *testing.T) {
		// 同一结果集两行：第二行使行数达到 maxRows=1 而触发上限。
		twoRows := w12gFakeQuery{match: `FROM "juhe_dataset"."model_check_runs"`, rows: &w12gFakeRows{columns: []string{"id", "score"}, rows: [][]driver.Value{{"r1", int64(7)}, {"r2", int64(8)}}}}
		conn := build(twoRows, targetEmpty)
		_, tx := w12gBeginTx(t, conn)
		_, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20})
		if err == nil || !strings.Contains(err.Error(), "exceeds max rows per table") {
			t.Fatalf("超行上限必须失败: %v", err)
		}
	})

	t.Run("source row exceed max bytes", func(t *testing.T) {
		conn := build(sourceRows("r1-long-value", int64(7)))
		_, tx := w12gBeginTx(t, conn)
		_, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 10, maxBytes: 4})
		if err == nil || !strings.Contains(err.Error(), "exceeds max bytes per table") {
			t.Fatalf("超字节上限必须失败: %v", err)
		}
	})

	t.Run("target row check fails", func(t *testing.T) {
		conn := build(sourceRows("r1", int64(7)), w12gFakeQuery{match: `FROM "juhe_j3b"."model_check_runs" WHERE`, err: errW12GDriver})
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, options); err == nil || !strings.Contains(err.Error(), "check existing") {
			t.Fatalf("目标行检查失败必须被包装: %v", err)
		}
	})

	t.Run("conflicting target row fails", func(t *testing.T) {
		conflict := w12gFakeQuery{match: `FROM "juhe_j3b"."model_check_runs" WHERE`, rows: &w12gFakeRows{columns: []string{"id", "score"}, rows: [][]driver.Value{{"r1", int64(9)}}}}
		conn := build(sourceRows("r1", int64(7)), conflict)
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, options); err == nil || !strings.Contains(err.Error(), "backfill conflict") {
			t.Fatalf("目标行冲突必须失败: %v", err)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		conn := build(sourceRows("r1", int64(7)), targetEmpty)
		conn.execErr = errW12GDriver
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, options); err == nil || !strings.Contains(err.Error(), "insert J3b PostgreSQL backfill table") {
			t.Fatalf("插入失败必须被包装: %v", err)
		}
	})

	t.Run("prepare fails", func(t *testing.T) {
		conn := build(sourceRows("r1", int64(7)), targetEmpty)
		conn.prepareErr = true
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, options); err == nil || !strings.Contains(err.Error(), "prepare J3b PostgreSQL backfill table") {
			t.Fatalf("prepare 失败必须被包装: %v", err)
		}
	})
}

func TestW12GBackfillPostgresTrustStateScripted(t *testing.T) {
	ctx := context.Background()
	exists := func() w12gFakeQuery {
		return w12gFakeQuery{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}}
	}
	// 每次生成新实例：w12gFakeRows 的 pos 有状态，脚本间不能共享指针。
	sourceShape := func() *w12gFakeRows {
		return &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"scope_type", "text", "text", "NO", nil, "NO"},
			{"scope_id", "text", "text", "NO", nil, "NO"},
			{"job_name", "text", "text", "NO", nil, "NO"},
			{"cursor_created_at", "text", "text", "YES", nil, "NO"},
			{"cursor_id", "text", "text", "YES", nil, "NO"},
			{"last_success_at", "text", "text", "YES", nil, "NO"},
			{"last_error_message", "text", "text", "YES", nil, "NO"},
			{"lag_seconds", "integer", "int4", "YES", nil, "NO"},
			{"updated_at", "text", "text", "NO", nil, "NO"},
		}}
	}
	targetShape := func() *w12gFakeRows {
		return &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"scope_key", "text", "text", "NO", nil, "NO"},
			{"cursor_created_at", "text", "text", "YES", nil, "NO"},
			{"cursor_id", "text", "text", "YES", nil, "NO"},
			{"last_success_at", "text", "text", "YES", nil, "NO"},
			{"last_error_message", "text", "text", "YES", nil, "NO"},
			{"lag_seconds", "integer", "int4", "YES", nil, "NO"},
			{"updated_at", "text", "text", "NO", nil, "NO"},
		}}
	}
	cursorRow := []driver.Value{"2026-08-27T10:00:00Z", "obs-1", "2026-08-27T10:01:00Z", nil, int64(3), "2026-08-27T10:01:00Z"}
	cursorRows := func(values ...[]driver.Value) w12gFakeQuery {
		return w12gFakeQuery{match: "FROM juhe_stats.stats_job_state WHERE scope_type=$1", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: values}}
	}
	opts := normalizedPostgresBackfillOptions{maxRows: 10, maxBytes: 1 << 20}

	t.Run("happy path insert", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
			cursorRows(cursorRow),
			{match: `FROM juhe_j3b.model_trust_aggregation_state WHERE scope_key=$1`, rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		result, err := backfillPostgresTrustAggregationState(ctx, tx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if result.SourceRows != 1 || result.InsertedRows != 1 {
			t.Fatalf("游标应复制一次: %+v", result)
		}
	})

	t.Run("cursor conflict fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
			cursorRows(cursorRow),
			{match: `FROM juhe_j3b.model_trust_aggregation_state WHERE scope_key=$1`, rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"scope", "other", "other", nil, nil, nil, "other"}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, opts); err == nil || !strings.Contains(err.Error(), "backfill conflict in model_trust_aggregation_state") {
			t.Fatalf("游标冲突必须失败: %v", err)
		}
	})

	t.Run("cursor read fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
			{match: "FROM juhe_stats.stats_job_state WHERE scope_type=$1", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, opts); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL trust aggregation cursor") {
			t.Fatalf("游标读取失败必须被包装: %v", err)
		}
	})

	t.Run("cursor exceeds max rows", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
			cursorRows(cursorRow, cursorRow),
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "exceeds max rows") {
			t.Fatalf("游标超行上限必须失败: %v", err)
		}
	})

	t.Run("cursor exceeds max bytes", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
			{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
			cursorRows(cursorRow),
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, normalizedPostgresBackfillOptions{maxRows: 10, maxBytes: 4}); err == nil || !strings.Contains(err.Error(), "exceeds max bytes") {
			t.Fatalf("游标超字节上限必须失败: %v", err)
		}
	})

	t.Run("iterate failures", func(t *testing.T) {
		type rowsPatch struct {
			nextErr  error
			closeErr error
		}
		build2 := func(patch rowsPatch) *w12gFakeConn {
			return &w12gFakeConn{queries: []w12gFakeQuery{
				exists(), exists(),
				{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: sourceShape()},
				{match: "information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position", rows: targetShape()},
				{match: "FROM juhe_stats.stats_job_state WHERE scope_type=$1", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{cursorRow}, nextErr: patch.nextErr, closeErr: patch.closeErr}},
				{match: `FROM juhe_j3b.model_trust_aggregation_state WHERE scope_key=$1`, rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}}},
			}}
		}
		_, tx := w12gBeginTx(t, build2(rowsPatch{nextErr: errW12GDriver}))
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, opts); err == nil || !strings.Contains(err.Error(), "iterate") {
			t.Fatalf("游标迭代失败必须被包装: %v", err)
		}
	})
}
