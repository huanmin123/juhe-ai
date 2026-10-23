package main

// 临时调试工装：抓取 chain fixture 上执行的全部 SQL，定位
// 「no such column: provider_code」来源。定位后立即删除本文件。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

var traceMu sync.Mutex

const traceLogPath = `C:/sub2api-lite/.local/tmp_trace_sql.log`

func logTraceSQL(kind, query string) {
	traceMu.Lock()
	defer traceMu.Unlock()
	f, err := os.OpenFile(traceLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = io.WriteString(f, kind+" | "+strings.ReplaceAll(strings.ReplaceAll(query, "\n", " "), "\t", " ")+"\n")
}

type traceDrv struct{ inner driver.Driver }

func (d traceDrv) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return traceConn{inner: c}, nil
}

type traceConn struct{ inner driver.Conn }

func (c traceConn) Prepare(query string) (driver.Stmt, error) {
	logTraceSQL("PREPARE", query)
	return c.inner.Prepare(query)
}
func (c traceConn) Close() error   { return c.inner.Close() }
func (c traceConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c traceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	logTraceSQL("EXEC", query)
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, query, args)
	}
	return nil, fmt.Errorf("trace: inner conn lacks ExecerContext")
}

func (c traceConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	logTraceSQL("QUERY", query)
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, query, args)
	}
	return nil, fmt.Errorf("trace: inner conn lacks QueryerContext")
}

func TestZZTraceSQLCapture(t *testing.T) {
	_ = os.Remove(traceLogPath)
	sql.Register("sqlite_zztrace", traceDrv{inner: &sqlite.Driver{}})
	db, err := sql.Open("sqlite_zztrace", t.TempDir()+"/business.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedChainBusinessSchema(t, db)
	statsDB, err := sql.Open("sqlite", t.TempDir()+"/stats.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer statsDB.Close()
	seedChainStatsSchema(t, statsDB)
	fixture := &chainFixture{db: db, statsDB: statsDB, systemAccount: "sys_owner", groupID: "group_main", accountID: "acc_1"}
	fixture.apiKeySecret = seedChainRuntimeRows(t, db, fixture)

	models, err := gatewayruntimecache.NewSQLReadModels(db, false, time.Now, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := newChainAccountsSelectorWithStats(db, statsDB, false, "chain-test-secret", time.Now, 20)
	if err != nil {
		t.Fatal(err)
	}
	models.SetAccountsSelector(selector)
	catalogSource, err := newChainCatalogSource(db, false)
	if err != nil {
		t.Fatal(err)
	}
	models.SetCatalogSource(catalogSource)
	models.SetConcurrencySource(gatewayclientip.NewMemoryAccountConcurrency(nil))
	unionLoader, err := newChainGroupModelUnionLoader(selector)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := gatewayruntimecache.New(models, gatewayruntimecache.Options{Clock: gatewayruntimecache.SystemClock(), Union: unionLoader})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	validator := &chainAPIKeyValidator{cache: cache}
	if _, err := validator.Validate(context.Background(), fixture.apiKeySecret); err != nil {
		t.Logf("validate error: %v", err)
	}
}
