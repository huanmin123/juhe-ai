package tablemonitor

// 表存储监控读面契约测试：overview 的 latest-per-(role,table) 快照、
// keyword 前缀过滤、分页上界，history 的窗口与倒序回正，database-history
// 的按角色合并升序（Node table-monitor.repository.ts）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const monitorSchema = `
	CREATE TABLE table_storage_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT, database_role TEXT NOT NULL, table_name TEXT NOT NULL,
		sampled_at TEXT NOT NULL, table_kind TEXT, parent_table_name TEXT, is_partition INTEGER DEFAULT 0,
		is_archive INTEGER DEFAULT 0, row_count INTEGER, table_bytes INTEGER, index_bytes INTEGER,
		total_bytes INTEGER, growth_bytes_1h INTEGER, growth_rows_1h INTEGER,
		growth_bytes_24h INTEGER, growth_rows_24h INTEGER);
	CREATE TABLE database_storage_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT, database_role TEXT NOT NULL, database_path TEXT NOT NULL,
		sampled_at TEXT NOT NULL, file_bytes INTEGER, wal_bytes INTEGER, shm_bytes INTEGER,
		free_bytes INTEGER, table_count INTEGER);
`

func newMonitorFixture(t *testing.T) (*Deps, *Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(monitorSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed := []string{
		// 每角色两轮快照，overview 只取最新一轮。
		`INSERT INTO database_storage_snapshots (database_role, database_path, sampled_at, file_bytes, wal_bytes, free_bytes, table_count) VALUES
			('business', '/data/business.sqlite3', '2026-09-03T00:00:00.000Z', 100, 10, 5, 12),
			('business', '/data/business.sqlite3', '2026-09-04T00:00:00.000Z', 200, 20, 6, 13),
			('stats', '/data/stats.sqlite3', '2026-09-04T00:00:00.000Z', 50, 5, 2, 7)`,
		`INSERT INTO table_storage_snapshots (database_role, table_name, sampled_at, row_count, table_bytes, index_bytes, total_bytes, growth_bytes_24h, growth_rows_24h) VALUES
			('business', 'accounts', '2026-09-03T00:00:00.000Z', 10, 100, 20, 120, 0, 0),
			('business', 'accounts', '2026-09-04T00:00:00.000Z', 20, 200, 50, 250, 130, 10),
			('business', 'groups', '2026-09-04T00:00:00.000Z', 5, 50, 10, 60, 5, 1),
			('stats', 'usage_stats_daily', '2026-09-04T00:00:00.000Z', 99, 900, 100, 1000, 100, 9)`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	store, err := NewStore(db, false)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	deps := &Deps{Store: store, Cache: NewOverviewCache()}
	return deps, store
}

func adminHandler(deps *Deps, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	auth := &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", Role: "admin"}
	request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	handler := http.HandlerFunc(deps.overviewHandler)
	// 直接驱动（requireAdmin 由 authsys 包测试覆盖）。
	handler(recorder, request)
	return recorder
}

func TestOverviewLatestPerRoleAndTable(t *testing.T) {
	deps, _ := newMonitorFixture(t)
	recorder := adminHandler(deps, "/__aisys__/api/table-monitor/overview")
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			SampledAt string `json:"sampledAt"`
			Databases []struct {
				DatabaseRole string `json:"databaseRole"`
				DatabasePath string `json:"databasePath"`
				FileBytes    *int   `json:"fileBytes"`
			} `json:"databases"`
			Tables []struct {
				DatabaseRole      string  `json:"databaseRole"`
				TableName         string  `json:"tableName"`
				RowCount          float64 `json:"rowCount"`
				TotalBytes        float64 `json:"totalBytes"`
				IndexToTableRatio float64 `json:"indexToTableRatio"`
				GrowthBytes24h    float64 `json:"growthBytes24h"`
			} `json:"tables"`
			Total   int  `json:"total"`
			HasMore bool `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Data.SampledAt != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("sampledAt wrong: %q", payload.Data.SampledAt)
	}
	// 数据库面按角色排序：business 在 stats 前，且只保留最新快照。
	if len(payload.Data.Databases) != 2 {
		t.Fatalf("databases wrong: %#v", payload.Data.Databases)
	}
	if payload.Data.Databases[0].DatabaseRole != "business" || payload.Data.Databases[0].FileBytes == nil || *payload.Data.Databases[0].FileBytes != 200 {
		t.Fatalf("business snapshot wrong: %#v", payload.Data.Databases[0])
	}
	if payload.Data.Databases[0].DatabasePath != "business.sqlite3" {
		t.Fatalf("databasePath should be basename: %q", payload.Data.Databases[0].DatabasePath)
	}
	// 表面按 total_bytes DESC；只出现每个 (role,table) 的最新快照。
	if len(payload.Data.Tables) != 3 {
		t.Fatalf("tables wrong: %#v", payload.Data.Tables)
	}
	if payload.Data.Tables[0].TableName != "usage_stats_daily" {
		t.Fatalf("largest table wrong: %#v", payload.Data.Tables[0])
	}
	accounts := payload.Data.Tables[1]
	if accounts.TableName != "accounts" || accounts.RowCount != 20 || accounts.GrowthBytes24h != 130 {
		t.Fatalf("accounts snapshot wrong: %#v", accounts)
	}
	if accounts.IndexToTableRatio != 0.25 {
		t.Fatalf("index ratio wrong: %#v", accounts.IndexToTableRatio)
	}
	if payload.Data.Total != 3 || payload.Data.HasMore {
		t.Fatalf("pagination wrong: %d %v", payload.Data.Total, payload.Data.HasMore)
	}
	// keyword 前缀过滤。
	recorder = adminHandler(deps, "/__aisys__/api/table-monitor/overview?keyword=acc")
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode keyword: %v", err)
	}
	if len(payload.Data.Tables) != 1 || payload.Data.Tables[0].TableName != "accounts" {
		t.Fatalf("keyword filter wrong: %#v", payload.Data.Tables)
	}
}

func TestTableHistoryWindow(t *testing.T) {
	store, _ := newMonitorFixture(t)
	points, err := store.Store.LoadTableHistory(context.Background(), "business", "accounts",
		"2026-08-05T00:00:00.000Z", "2026-09-04T23:00:00.000Z", 720)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	// 升序回正：旧 -> 新。
	if len(points) != 2 || points[0].SampledAt != "2026-09-03T00:00:00.000Z" || points[1].SampledAt != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("history wrong: %#v", points)
	}
}

func TestDatabaseHistoryMergesRolesAscending(t *testing.T) {
	store, _ := newMonitorFixture(t)
	points, err := store.Store.LoadDatabaseHistory(context.Background(),
		"2026-08-05T00:00:00.000Z", "2026-09-04T23:00:00.000Z", 720)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	// 同一时刻多个角色并作相邻行，同刻按角色序 business < stats。
	if len(points) != 3 {
		t.Fatalf("points wrong: %#v", points)
	}
	if points[0].SampledAt != "2026-09-03T00:00:00.000Z" || points[1].SampledAt != "2026-09-04T00:00:00.000Z" || points[2].SampledAt != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("ordering wrong: %#v", points)
	}
	if points[1].DatabaseRole != "business" || points[2].DatabaseRole != "stats" {
		t.Fatalf("role tiebreak wrong: %#v", points)
	}
}

func TestParseHistoryWindowDefaultsAndSwap(t *testing.T) {
	if _, err := NewStore(nil, false); err == nil {
		t.Fatalf("nil db should fail")
	}
	deps := &Deps{Store: &Store{now: func() (string, int64) {
		base := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
		return base.Format("2006-01-02T15:04:05.000Z"), base.UnixMilli()
	}}}
	// 显式倒序窗口会被交换。
	startAt, endAt, badRequest := deps.parseHistoryWindow(func() url.Values {
		values := url.Values{}
		values.Set("startAt", "2026-09-04T00:00:00.000Z")
		values.Set("endAt", "2026-09-01T00:00:00.000Z")
		return values
	}())
	if badRequest != "" || startAt != "2026-09-01T00:00:00.000Z" || endAt != "2026-09-04T00:00:00.000Z" {
		t.Fatalf("swap wrong: %q %q %q", badRequest, startAt, endAt)
	}
	// 缺省窗口 = 近 30 天。
	startAt, endAt, badRequest = deps.parseHistoryWindow(url.Values{})
	if badRequest != "" || startAt != "2026-08-05T12:00:00.000Z" || endAt != "2026-09-04T12:00:00.000Z" {
		t.Fatalf("default window wrong: %q %q %q", badRequest, startAt, endAt)
	}
	// 非法时间。
	_, _, badRequest = deps.parseHistoryWindow(func() url.Values { values := url.Values{}; values.Set("startAt", "not-a-time"); return values }())
	if badRequest == "" {
		t.Fatalf("invalid time not rejected")
	}
}

var _ = time.Now
