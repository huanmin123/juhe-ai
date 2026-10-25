package main

// w14k 波次：storage bootstrap 与 schema snapshot 会话层的剩余分支。runner
// 主体已在 wm_cli_runners_test.go / w12g_storage_direct_test.go / wm_snapshot_
// session_test.go 覆盖；本文件补 encode 失败与 runSnapshotSession 事务错误
// 注入。ensureSQLiteStorage 失败点与 paths 解析用例随 SQLite 存储形态退役
// 删除（PLAN-20261024T051925945Z B4）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
)

func TestW14KStorageBootstrapEncodeFailure(t *testing.T) {
	wm14kWithClosedStdout(t, func() {
		if code := runStorageBootstrap(true, true, "postgres://wm@127.0.0.1:1/no-such-db", ""); code != 1 {
			t.Fatalf("不可达库必须返回 1: %d", code)
		}
	})
}

// w14kFailConn 在 wm snapshot fake 基础上按标记注入事务层错误，覆盖
// runSnapshotSession 的 BeginTx / set_config / Commit 错误出口。
type w14kFailConn struct {
	routes     map[string]wmSnapshotResult
	failBegin  bool
	failExec   bool
	failCommit bool
}

func (c *w14kFailConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w14k fail fake: Prepare 不应被调用")
}

func (c *w14kFailConn) Close() error { return nil }

func (c *w14kFailConn) Begin() (driver.Tx, error) {
	return nil, errors.New("w14k fail fake: Begin 不应被调用（走 BeginTx）")
}

type w14kFailTx struct{ failCommit bool }

func (t w14kFailTx) Commit() error {
	if t.failCommit {
		return errors.New("w14k fail fake: commit 失败")
	}
	return nil
}

func (t w14kFailTx) Rollback() error { return nil }

func (c *w14kFailConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if c.failBegin {
		return nil, errors.New("w14k fail fake: begin 失败")
	}
	return w14kFailTx{failCommit: c.failCommit}, nil
}

func (c *w14kFailConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "set_config(") {
		if c.failExec {
			return nil, errors.New("w14k fail fake: set_config 失败")
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("w14k fail fake: unexpected exec")
}

func (c *w14kFailConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	for key, result := range c.routes {
		if strings.Contains(query, key) {
			return &wmSnapshotRows{columns: result.columns, rows: result.rows}, nil
		}
	}
	return nil, errors.New("w14k fail fake: unexpected query")
}

type w14kFailConnector struct{ conn *w14kFailConn }

func (c w14kFailConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c w14kFailConnector) Driver() driver.Driver                        { return wmSnapshotDriver{} }

func TestW14KRunSnapshotSessionFailurePoints(t *testing.T) {
	base := func() *w14kFailConn {
		return &w14kFailConn{routes: wmSnapshotRoutes()}
	}
	t.Run("begin fails", func(t *testing.T) {
		conn := base()
		conn.failBegin = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("BeginTx 失败必须上抛")
		}
	})
	t.Run("set_config fails", func(t *testing.T) {
		conn := base()
		conn.failExec = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("set_config 失败必须上抛")
		}
	})
	t.Run("commit fails", func(t *testing.T) {
		conn := base()
		conn.failCommit = true
		db := sql.OpenDB(w14kFailConnector{conn: conn})
		defer db.Close()
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
			t.Fatal("Commit 失败必须上抛")
		}
	})
}

func TestW14KWriteSnapshotJSONEncodeFailure(t *testing.T) {
	wm14kWithClosedStdout(t, func() {
		if err := writeSnapshotJSON(schemasnapshot.SchemaSnapshot{SchemaVersion: 1}); err == nil {
			t.Fatal("stdout 关闭时 encode 必须失败")
		}
	})
}
