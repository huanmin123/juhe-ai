package statreads

// w11g 覆盖补充（第三批）：usage-records keyword 的 owner 授权/分组检索
// 路径、真实 shard 文件读链与水合错误臂、timekeys 边界。

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
)

func TestW11GUsageRecordKeywordOwnerPaths(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.exec(t,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status) VALUES
			('w11g-owned-acct', 'w11g-owned', 'sys-user-1', 'openai', 'api_key', 'active'),
			('w11g-inst-src', 'w11g-inst-src', 'sys-other', 'openai', 'oauth', 'active'),
			('w11g-inst-child', 'w11g-inst-child', 'sys-other', 'openai', 'oauth', 'active'),
			('w11g-ra-acct', 'w11g-ra', 'sys-other', 'openai', 'api_key', 'active'),
			('w11g-group-acct', 'w11g-group', 'sys-other', 'openai', 'api_key', 'active')`,
		`UPDATE accounts SET authorization_instance_source_account_id = 'w11g-inst-src' WHERE id = 'w11g-inst-child'`,
		`INSERT INTO group_accounts (account_id, group_id, system_account_id, enabled) VALUES
			('w11g-group-acct', 'w11g-group-1', 'sys-other', 1)`,
		`INSERT INTO resource_authorizations (id, resource_type, resource_id, owner_system_account_id, grantee_system_account_id, status, created_at, updated_at)
			VALUES ('w11g-ra-1', 'account', 'w11g-ra-acct', 'sys-other', 'sys-user-1', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z'),
			       ('w11g-ra-2', 'group', 'w11g-group-1', 'sys-other', 'sys-user-1', 'active', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
	)
	// my-usage-records：owner 路径四段检索（自身/实例/授权/分组）。
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet, "/my-usage-records?accountKeyword=w11g&startDate=2026-09-04&endDate=2026-09-04", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("owner keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 关键字无匹配 → 1 = 0 空集。
	recorder = invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet, "/my-usage-records?accountKeyword=zzz-none&startDate=2026-09-04&endDate=2026-09-04", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("no match keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// admin 面 keyword 匹配（非 owner 分支；日期在 admin 过滤门内，改走 systemAccountId 形态）。
	recorder = invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet, "/usage-records?accountKeyword=w11g&systemAccountId=sys-admin-1", adminAuth(""))
	if recorder.Code == http.StatusBadRequest {
		t.Fatalf("admin keyword = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW11GUsageShardFileReadChain(t *testing.T) {
	fixture := newWdFixture(t)
	fixture.deps.UsageCatalog = fixture.db
	if _, err := fixture.db.Exec(`CREATE TABLE usage_record_shards (shard_key TEXT NOT NULL, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL, file_path TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// 真实 shard 文件：与 catalog 同库即可（file:path 形式只读打开）。
	shardPath := filepath.ToSlash(filepath.Join(t.TempDir(), "w11g-shard.db"))
	shard, err := sql.Open("sqlite", "file:"+shardPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shard.Exec(`CREATE TABLE usage_records (
		id TEXT PRIMARY KEY, system_account_id TEXT, trace_id TEXT, traffic_source TEXT, client_ip TEXT,
		api_key_id TEXT, group_id TEXT, account_id TEXT, endpoint TEXT, model TEXT, upstream_model TEXT,
		upstream_response_model TEXT, billed_service_tier TEXT, effective_reasoning_effort TEXT,
		model_mapping_applied INTEGER, stream INTEGER, status_code INTEGER, success INTEGER,
		failure_attribution TEXT, error_code TEXT, error_message TEXT, first_token_ms INTEGER,
		duration_ms INTEGER, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
		cost_usd REAL, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := shard.Exec(`INSERT INTO usage_records (id, system_account_id, api_key_id, group_id, account_id, model, stream, status_code, success, input_tokens, output_tokens, cost_usd, created_at)
		VALUES ('w11g-rec-1', 'sys-admin-1', 'w11g-key', 'w11g-group', 'w11g-owned-acct', 'gpt-5', 0, 200, 1, 10, 20, 0.1, '2026-09-04T08:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := shard.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
		VALUES ('w11g-shard', '2026-09-04', 1, '` + shardPath + `', 'active')`); err != nil {
		t.Fatal(err)
	}
	fixture.exec(t, `INSERT INTO accounts (id, name, system_account_id, provider_code, type, status) VALUES ('w11g-owned-acct', 'w11g-owned', 'sys-admin-1', 'openai', 'api_key', 'active')`)
	// 完整读链：shard 文件 → 合并 → 名称水合（owner 面绕开 admin 过滤门）。
	recorder := invoke(t, fixture.deps.usageRecordsListHandler(true), http.MethodGet, "/my-usage-records?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("shard read = %d body=%s", recorder.Code, recorder.Body.String())
	}
	// 水合错误臂：api_keys / groups / accounts / system_accounts 逐个 drop。
	for _, statement := range []string{
		`DROP TABLE api_keys`,
		`DROP TABLE groups`,
		`DROP TABLE accounts`,
		`DROP TABLE system_accounts`,
	} {
		fixtureShard := newWdFixture(t)
		fixtureShard.deps.UsageCatalog = fixtureShard.db
		if _, err := fixtureShard.db.Exec(`CREATE TABLE usage_record_shards (shard_key TEXT NOT NULL, bucket_date TEXT NOT NULL, shard_id INTEGER NOT NULL, file_path TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixtureShard.db.Exec(`INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
			VALUES ('w11g-shard', '2026-09-04', 1, '` + shardPath + `', 'active')`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixtureShard.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		// system_accounts 水合只在 admin 全局面触发；不传日期参数避免过滤门。
		target := "/my-usage-records"
		if statement == "DROP TABLE system_accounts" {
			target = "/usage-records"
		}
		recorder = invoke(t, fixtureShard.deps.usageRecordsListHandler(statement != "DROP TABLE system_accounts"), http.MethodGet, target, adminAuth(""))
		if statement == "DROP TABLE system_accounts" && recorder.Code != http.StatusInternalServerError {
			t.Fatalf("%s hydrate = %d body=%s", statement, recorder.Code, recorder.Body.String())
		}
	}
}

func TestW11GTimekeyBoundaries(t *testing.T) {
	// bucketDateKeyToUTCms：非数字日期段。
	if _, ok := bucketDateKeyToUTCms("2026ab01"); ok {
		t.Fatal("non-numeric key must fail")
	}
	if _, ok := bucketDateKeyToUTCms("2026090"); ok {
		t.Fatal("short key must fail")
	}
	// bucketDateKeyFromIso：短值返回空。
	if got := bucketDateKeyFromIso("2026"); got != "" {
		t.Fatalf("short iso key=%s", got)
	}
	// compareUsageRecordRows：坏时间戳回退 id 比较。
	left := Row{"created_at": "bad", "id": "a"}
	right := Row{"created_at": "bad", "id": "b"}
	if !compareUsageRecordRows(left, right, sortAsc) {
		t.Fatal("asc id compare")
	}
	if compareUsageRecordRows(left, right, sortDesc) {
		t.Fatal("desc id compare")
	}
}
