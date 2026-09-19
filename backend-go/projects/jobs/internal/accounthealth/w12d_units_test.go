package accounthealth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w12d_units_test.go 补齐 w12d 波次的配置加载矩阵、凭据 envelope 解码臂、
// 签名 request/input 文件读取臂、显式探针 executor 早退臂与 PG direct input
// 的候选装配错误臂。全部为纯单元路径，不触网。

// TestW12dLoadConfigNilGetenv 覆盖 getenv 注入为 nil 的分支：nil 等价
// os.Getenv，在完整 env 下正常通过（恒开终态，无 disabled 默认路径）。
func TestW12dLoadConfigNilGetenv(t *testing.T) {
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_STORE", "sqlite")
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH", filepath.Join(t.TempDir(), "j1.sqlite3"))
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", t.TempDir())
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY")
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", "w12d-secret")
	config, err := LoadConfig(nil)
	if err != nil || config.Store.Mode != StoreSQLite {
		t.Fatalf("nil getenv must behave like os.Getenv: %+v %v", config, err)
	}
}

// TestW12dLoadConfigFullPostgresMatrix 覆盖 postgres store + postgres direct
// input 的完整 happy path 与各显式 env 解析。
func TestW12dLoadConfigFullPostgresMatrix(t *testing.T) {
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	set := func(t *testing.T, values map[string]string) {
		t.Helper()
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER", "go")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID", "w12d-instance")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_STORE", "postgres")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL", "postgres://w12d/w12d")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", t.TempDir())
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY", key)
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", "w12d-secret")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE", "postgres")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL", "postgres://w12d/business")
		for name, value := range values {
			t.Setenv(name, value)
		}
	}
	base := map[string]string{}
	set(t, base)
	config, err := LoadConfig(os.Getenv)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if config.Store.Mode != StorePostgres || config.InputSource != "postgres" {
		t.Fatalf("config=%+v", config)
	}
	if config.Store.PostgresMaxOpenConns != defaultPostgresPoolSize || config.DirectInputPostgresMaxIdleConns != defaultPostgresMaxIdleConns {
		t.Fatalf("pool defaults: %+v", config)
	}
	if config.InputTTL != defaultInputTTL || config.ScanInterval != defaultScanInterval || config.OwnerLease != defaultOwnerLease || config.ProbeTimeout != defaultProbeTimeout {
		t.Fatalf("duration defaults: %+v", config)
	}
	if config.MaxResponseBytes != defaultMaxResponseBytes || config.MaxConcurrency != defaultPostgresConcurrency {
		t.Fatalf("concurrency defaults: %+v", config)
	}
	if config.IOConcurrency != config.MaxConcurrency || config.DBConcurrency != defaultDBConcurrency || config.DBQueueSize != defaultDBQueueSize {
		t.Fatalf("worker defaults: %+v", config)
	}
	if config.InputKeys["runtime-v1"] == nil {
		t.Fatal("default key id must apply")
	}
	// 显式 env 值覆盖默认值并覆盖全部解析分支。
	set(t, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY_ID":      "w12d-key",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS":   "8",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS":   "4",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "6",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS": "2",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS":              "7200000",
		"JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL":             "10s",
		"JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE":               "60s",
		"JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT":             "5s",
		"JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES":        "4096",
		"JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY":           "16",
		"JUHE_AI_ACCOUNT_HEALTH_IO_CONCURRENCY":            "8",
		"JUHE_AI_ACCOUNT_HEALTH_DB_CONCURRENCY":            "4",
		"JUHE_AI_ACCOUNT_HEALTH_DB_QUEUE_SIZE":             "64",
		"JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT":        "32",
	})
	config, err = LoadConfig(os.Getenv)
	if err != nil {
		t.Fatalf("explicit env: %v", err)
	}
	if config.Store.PostgresMaxOpenConns != 8 || config.Store.PostgresMaxIdleConns != 4 || config.DirectInputPostgresMaxOpenConns != 6 || config.DirectInputPostgresMaxIdleConns != 2 {
		t.Fatalf("explicit pool: %+v", config)
	}
	if config.InputTTL != 2*time.Hour || config.ScanInterval != 10*time.Second || config.OwnerLease != time.Minute || config.ProbeTimeout != 5*time.Second {
		t.Fatalf("explicit durations: %+v", config)
	}
	if config.MaxResponseBytes != 4096 || config.MaxConcurrency != 16 || config.IOConcurrency != 8 || config.DBConcurrency != 4 || config.DBQueueSize != 64 || config.DirectInputLimit != 32 {
		t.Fatalf("explicit workers: %+v", config)
	}
	if config.InputKeys["w12d-key"] == nil {
		t.Fatal("explicit key id must apply")
	}
}

// TestW12dLoadConfigErrorMatrix 覆盖配置校验的拒绝分支。
func TestW12dLoadConfigErrorMatrix(t *testing.T) {
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	base := func(t *testing.T) {
		t.Helper()
		t.Setenv("JUHE_AI_SECRET", "")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER", "go")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID", "w12d-instance")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_STORE", "postgres")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL", "postgres://w12d/w12d")
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", t.TempDir())
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY", key)
		t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", "w12d-secret")
	}
	cases := []struct {
		name   string
		env    map[string]string
		frag   string
	}{
		{"owner not go", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER": "node"}, "JOBS_OWNER"},
		{"bad store", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_STORE": "mysql"}, "sqlite 或 postgres"},
		{"sqlite missing path", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_STORE": "sqlite", "JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH": ""}, "DATABASE_PATH"},
		{"postgres missing url", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL": ""}, "POSTGRES_URL"},
		{"pool invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS": "2", "JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS": "4"}, "连接池配置无效"},
		{"pool zero", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS": "0"}, "必须是正整数"},
		{"missing input dir", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY": ""}, "INPUT_DIRECTORY"},
		{"sqlite db inside input dir", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_STORE": "sqlite", "JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY": filepath.Join(rootOnce(), "w12d-inputs"), "JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH": filepath.Join(rootOnce(), "w12d-inputs", "x.sqlite3")}, "不得放入 input 目录"},
		{"bad input source", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE": "redis"}, "files 或 postgres"},
		{"pg input with sqlite store", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_STORE": "sqlite", "JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH": filepath.Join(t.TempDir(), "jobs", "x.sqlite3"), "JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE": "postgres"}, "只允许与 postgres"},
		{"missing input pg url", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE": "postgres", "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": ""}, "INPUT_POSTGRES_URL"},
		{"input pool invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE": "postgres", "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://w12d/b", "JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "-1"}, "必须是正整数"},
		{"missing signing key", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": ""}, "SIGNING_KEY"},
		{"short signing key", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "aaaa"}, "至少 32 字节"},
		{"bad signing key", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "!!!"}, "至少 32 字节"},
		{"missing credential secret", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": ""}, "CREDENTIAL_SECRET"},
		{"input ttl below min", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS": "1"}, "必须在"},
		{"input ttl above max", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_TTL_MS": "999999999999"}, "必须在"},
		{"scan interval invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL": "1s"}, "duration"},
		{"owner lease invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE": "1s"}, "duration"},
		{"lease below timeout", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE": "15s", "JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT": "60s"}, "必须大于"},
		{"max response invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES": "0"}, "必须在"},
		{"concurrency below min", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY": "0"}, "必须在"},
		{"io concurrency invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_IO_CONCURRENCY": "-3"}, "必须在"},
		{"db concurrency invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_DB_CONCURRENCY": "x"}, "必须在"},
		{"db queue invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_DB_QUEUE_SIZE": "99999"}, "必须在"},
		{"direct input limit invalid", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT": "99999"}, "必须在"},
		{"direct input limit zero", map[string]string{"JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT": "0"}, "必须在"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			if _, err := LoadConfig(os.Getenv); err == nil || !strings.Contains(err.Error(), tc.frag) {
				t.Fatalf("err=%v want frag=%q", err, tc.frag)
			}
		})
	}
}

// TestW12dLoadConfigSQLiteIsolationArms 覆盖 SQLite 共享文件隔离检查。
func TestW12dLoadConfigSQLiteIsolationArms(t *testing.T) {
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	root := t.TempDir()
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER", "go")
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID", "w12d-instance")
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_STORE", "sqlite")
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH", filepath.Join(root, "jobs.sqlite3"))
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY", filepath.Join(root, "inputs"))
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY", key)
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", "w12d-secret")
	// 与既有 runtime 库同文件 → 拒绝。
	t.Setenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH", filepath.Join(root, "jobs.sqlite3"))
	if _, err := LoadConfig(os.Getenv); err == nil || !strings.Contains(err.Error(), "共用 SQLite 文件") {
		t.Fatalf("shared file: %v", err)
	}
	t.Setenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "")
	// 大小写不同的同文件路径（Windows 语义）同样拒绝。
	t.Setenv("JUHE_AI_DATABASE_PATH", strings.ToUpper(filepath.Join(root, "jobs.sqlite3")))
	if _, err := LoadConfig(os.Getenv); err == nil {
		t.Skip("platform path folding differs")
	}
	t.Setenv("JUHE_AI_DATABASE_PATH", "")
	config, err := LoadConfig(os.Getenv)
	if err != nil {
		t.Fatalf("isolated sqlite: %v", err)
	}
	if config.Store.Mode != StoreSQLite || config.InputSource != "files" {
		t.Fatalf("config=%+v", config)
	}
}

// TestW12dDecryptV1EnvelopeBadBase64Arms 覆盖 envelope tag/ciphertext 解码失败臂。
func TestW12dDecryptV1EnvelopeBadBase64Arms(t *testing.T) {
	if _, err := DecryptV1Envelope("s", "v1:!!!:AAAA:AAAA"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad iv: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:!!!:AAAA"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad tag: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA:!!!"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad ciphertext: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA"); err == nil || !strings.Contains(err.Error(), "IV 或 tag 长度无效") {
		t.Fatalf("bad lengths: %v", err)
	}
}

// TestW12dLoadSignedProbeRequestsArms 覆盖签名 request 文件读取的全部错误臂。
func TestW12dLoadSignedProbeRequestsArms(t *testing.T) {
	key := []byte("w12d-request-signing-key-1234567")
	validRequest := ProbeRequest{RequestID: "w12d-req-1", AccountID: "w12d-acc", Reason: "activation", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Deadline: time.Now().UTC().Add(time.Minute)}
	validPayload, err := json.Marshal(validRequest)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("empty directory", func(t *testing.T) {
		if _, err := LoadSignedProbeRequests("  ", map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "目录缺失") {
			t.Fatalf("empty dir: %v", err)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if _, err := LoadSignedProbeRequests(filepath.Join(t.TempDir(), "absent"), map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "读取 account-health request 目录失败") {
			t.Fatalf("missing dir: %v", err)
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "a"+requestFileSuffix)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		// 目录项消失后重读失败难以稳定注入；改为验证坏签名臂。
		if err := os.WriteFile(path, []byte("not-signed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSignedProbeRequests(root, map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "验证") {
			t.Fatalf("bad signature: %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a"+requestFileSuffix), signedEnvelope(t, "current", key, []byte("{bad")), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSignedProbeRequests(root, map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "解析") {
			t.Fatalf("bad json: %v", err)
		}
	})
	t.Run("invalid fence", func(t *testing.T) {
		root := t.TempDir()
		invalid := validRequest
		invalid.InputVersion = 0
		payload, _ := json.Marshal(invalid)
		if err := os.WriteFile(filepath.Join(root, "a"+requestFileSuffix), signedEnvelope(t, "current", key, payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSignedProbeRequests(root, map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "无效") {
			t.Fatalf("invalid request: %v", err)
		}
	})
	t.Run("duplicate request id", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "b"} {
			if err := os.WriteFile(filepath.Join(root, name+requestFileSuffix), signedEnvelope(t, "current", key, validPayload), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := LoadSignedProbeRequests(root, map[string][]byte{"current": key}); err == nil || !strings.Contains(err.Error(), "重复 request ID") {
			t.Fatalf("duplicate: %v", err)
		}
	})
	t.Run("sorted happy path", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a"+requestFileSuffix), signedEnvelope(t, "current", key, validPayload), 0o600); err != nil {
			t.Fatal(err)
		}
		second := validRequest
		second.RequestID = "w12d-req-2"
		secondPayload, _ := json.Marshal(second)
		if err := os.WriteFile(filepath.Join(root, "b"+requestFileSuffix), signedEnvelope(t, "current", key, secondPayload), 0o600); err != nil {
			t.Fatal(err)
		}
		requests, err := LoadSignedProbeRequests(root, map[string][]byte{"current": key})
		if err != nil || len(requests) != 2 || requests[0].RequestID != "w12d-req-1" {
			t.Fatalf("requests=%+v err=%v", requests, err)
		}
	})
}

// TestW12dExecuteInputProbeEarlyArms 覆盖 executor 的显式请求早退臂。
func TestW12dExecuteInputProbeEarlyArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w12d-exec.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease := OwnerLease{OwnerID: "w12d-exec", FenceToken: 1}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	input := testInput("http://127.0.0.1:1", "chat_json")
	request := ProbeRequest{RequestID: "w12d-exec-1", AccountID: input.AccountID, Reason: "activation", InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision, Deadline: now.Add(time.Minute)}
	// deadline 已过。
	expired := request
	expired.Deadline = now.Add(-time.Minute)
	outcome, err := ExecuteInputProbe(ctx, store, lease, input, expired, ProbeOptions{Now: func() time.Time { return now }})
	if err != nil || outcome.ErrorCode != "request_deadline_elapsed" {
		t.Fatalf("expired deadline: %+v %v", outcome, err)
	}
	// oauth access 缺失。
	oauth := input
	oauth.Type = "oauth"
	outcome, err = ExecuteInputProbe(ctx, store, lease, oauth, request, ProbeOptions{Now: func() time.Time { return now }})
	if err != nil || outcome.ErrorCode != "oauth_access_missing" {
		t.Fatalf("oauth missing: %+v %v", outcome, err)
	}
	// api key pool 缺失。
	keyless := input
	keyless.KeySetFingerprint = ""
	outcome, err = ExecuteInputProbe(ctx, store, lease, keyless, request, ProbeOptions{Now: func() time.Time { return now }})
	if err != nil || outcome.ErrorCode != "api_key_pool_missing" {
		t.Fatalf("api keys missing: %+v %v", outcome, err)
	}
}

// TestW12dDirectInputToInputArms 表驱动覆盖 DirectInput.ToInput 的错误臂。
func TestW12dDirectInputToInputArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	secret := "w12d-direct-secret"
	envelope := testEnvelope(t, secret, `{"api_key":"sk-w12d"}`)
	valid := DirectInput{
		Account: DirectAccount{
			ID: "w12d-din", ConfigRevision: 5, DispatchRevision: 7, Provider: "deepseek",
			ProtocolProfileID: "profile_deepseek_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json",
			HealthModel: "w12d-model", CredentialsEncrypted: envelope,
		},
		Binding:      DirectBinding{GroupID: "w12d-group", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1",
	}
	input, err := valid.ToInput(secret, now)
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if input.AccountID != "w12d-din" || len(input.APIKeys) != 1 {
		t.Fatalf("input=%+v", input)
	}
	cases := []struct {
		name   string
		mutate func(*DirectInput)
		frag   string
	}{
		{"missing secret", func(d *DirectInput) {}, "缺少业务凭据 secret"},
		{"binding disabled", func(d *DirectInput) { d.Binding = DirectBinding{} }, "group binding"},
		{"bad epoch", func(d *DirectInput) { d.InputVersion = 0 }, "epoch 或有效期无效"},
		{"expired", func(d *DirectInput) { d.ExpiresAt = now.Add(-time.Minute) }, "epoch 或有效期无效"},
		{"missing tls policy", func(d *DirectInput) { d.TLSPolicy = " " }, "TLS policy"},
		{"unsupported protocol metadata", func(d *DirectInput) { d.Account.ProtocolCode = "gopher" }, "protocol_code 与 profile 不一致"},
		{"empty key pool", func(d *DirectInput) {
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"api_key":""}`)
		}, "API Key pool 为空"},
		{"oauth token missing", func(d *DirectInput) {
			d.Account.Type = "oauth"
			d.Account.Provider = "gpt"
			d.Account.ProtocolProfileID = "profile_gpt_openai_v1"
			d.Account.EndpointMode = "responses_json"
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"nope":1}`)
		}, "OAuth access token 缺失"},
		{"oauth token expired", func(d *DirectInput) {
			d.Account.Type = "oauth"
			d.Account.Provider = "gpt"
			d.Account.ProtocolProfileID = "profile_gpt_openai_v1"
			d.Account.EndpointMode = "responses_json"
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"access_token":"at","expires_at":"2026-09-06T12:00:30Z"}`)
		}, "已到期或接近到期"},
		{"undecryptable credentials", func(d *DirectInput) {
			d.Account.CredentialsEncrypted = "not-an-envelope"
		}, "凭据"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject := valid
			if tc.name == "missing secret" {
				if _, err := subject.ToInput("  ", now); err == nil || !strings.Contains(err.Error(), tc.frag) {
					t.Fatalf("err=%v want=%q", err, tc.frag)
				}
				return
			}
			tc.mutate(&subject)
			if _, err := subject.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), tc.frag) {
				t.Fatalf("err=%v want frag=%q", err, tc.frag)
			}
		})
	}
	// OAuth happy path（access token + account id 提取）。
	oauth := valid
	oauth.Account.Type = "oauth"
	oauth.Account.Provider = "gpt"
	oauth.Account.ProtocolProfileID = "profile_gpt_openai_v1"
	oauth.Account.EndpointMode = "responses_json"
	oauth.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"access_token":"at-w12d","expires_at":"2027-01-01T00:00:00Z","account_id":"acc-w12d"}`)
	input, err = oauth.ToInput(secret, now)
	if err != nil || input.OAuthAccess == nil || input.OAuthAccountID != "acc-w12d" {
		t.Fatalf("oauth input=%+v err=%v", input, err)
	}
	// Proxy envelope。
	withProxy := valid
	withProxy.Proxy = &DirectProxy{ID: "w12d-proxy", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080, PasswordEncrypted: testEnvelope(t, secret, "pw")}
	input, err = withProxy.ToInput(secret, now)
	if err != nil || input.Proxy == nil {
		t.Fatalf("proxy input=%+v err=%v", input, err)
	}
}

// rootOnce 返回一个稳定的临时根目录，供跨用例共享 input/db 路径。
func rootOnce() string { return os.TempDir() }
