package proxylatency

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖 J3a 运行时单元契约：RuntimeConfig 装载与 fail-closed 校验、
// DB 并发闸门、执行器代理 URL/凭据信封、传输失败码与目标 URL 规范化。

func wfEnv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestWFLoadRuntimeConfig(t *testing.T) {
	// 2026-09-21 起无总开关：J3a 依赖缺席（无任何 PG 连接串）时家族合法
	// 缺席，nil getenv 仍回退 os.Getenv。
	cfg, err := LoadRuntimeConfig(nil)
	if err != nil || cfg.Enabled {
		t.Fatalf("nil getenv 必须回退 os.Getenv 且依赖缺席合法: cfg=%+v err=%v", cfg, err)
	}
	cfg, err = LoadRuntimeConfig(wfEnv(nil))
	if err != nil || cfg.Enabled {
		t.Fatalf("依赖缺席配置 cfg=%+v err=%v", cfg, err)
	}
	if cfg.Now == nil {
		t.Fatal("Now 必须回填")
	}

	base := map[string]string{
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "wf-i",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        "postgres://jobs:x@127.0.0.1:5432/j",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://ro:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": "postgres://res:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "secret",
	}
	cases := []struct {
		name    string
		patch   map[string]string
		wantErr string
	}{
		{name: "owner 非 go", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_JOBS_OWNER": "node"}, wantErr: "JOBS_OWNER"},
		{name: "store 非 postgres", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_STORE": "sqlite"}, wantErr: "postgres jobs store"},
		// 缺 jobs URL 的失败臂已由零配置回退（JUHE_AI_POSTGRES_URL）取代，
		// 依赖缺席语义见 TestWFLoadRuntimeConfig 头部断言。
		{name: "连接池非数字", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_OPEN_CONNS": "abc"}, wantErr: "正整数"},
		{name: "缺业务 URL", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL": ""}, wantErr: "INPUT_POSTGRES_URL"},
		{name: "缺结果 URL", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": ""}, wantErr: "RESULT_POSTGRES_URL"},
		{name: "input limit 越界", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_INPUT_LIMIT": "0"}, wantErr: "有效范围"},
		{name: "batch size 越界", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_BATCH_SIZE": "99999"}, wantErr: "有效范围"},
		{name: "candidate factor 越界", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_CANDIDATE_POOL_FACTOR": "1001"}, wantErr: "有效范围"},
		{name: "db 并发越界", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_DB_CONCURRENCY": "65"}, wantErr: "有效范围"},
		{name: "input TTL 过短", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_INPUT_TTL": "30s"}, wantErr: "duration"},
		{name: "interval 过短", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_INTERVAL": "100ms"}, wantErr: "duration"},
		{name: "owner lease 过短", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_OWNER_LEASE": "1s"}, wantErr: "duration"},
		{name: "probe timeout 过长", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_PROBE_TIMEOUT": "16m"}, wantErr: "duration"},
		{name: "owner lease 不大于 probe", patch: map[string]string{
			"JUHE_AI_PROXY_LATENCY_OWNER_LEASE": "60s", "JUHE_AI_PROXY_LATENCY_PROBE_TIMEOUT": "120s",
		}, wantErr: "owner lease 必须大于"},
		{name: "owner lease 不大于 proxy", patch: map[string]string{
			"JUHE_AI_PROXY_LATENCY_OWNER_LEASE": "60s", "JUHE_AI_PROXY_LATENCY_PROXY_LEASE": "120s",
		}, wantErr: "owner lease 必须大于"},
		{name: "缺凭据密钥", patch: map[string]string{"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET": ""}, wantErr: "CREDENTIAL_SECRET"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for key, value := range base {
				env[key] = value
			}
			for key, value := range tt.patch {
				env[key] = value
			}
			_, err := LoadRuntimeConfig(wfEnv(env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v 必须包含 %q", err, tt.wantErr)
			}
		})
	}

	// 合法配置：显式值与默认值各覆盖一份。
	full := map[string]string{}
	for key, value := range base {
		full[key] = value
	}
	full["JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_OPEN_CONNS"] = "8"
	full["JUHE_AI_PROXY_LATENCY_POSTGRES_MAX_IDLE_CONNS"] = "4"
	full["JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_MAX_OPEN_CONNS"] = "6"
	full["JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_MAX_IDLE_CONNS"] = "3"
	full["JUHE_AI_PROXY_LATENCY_INPUT_LIMIT"] = "100"
	full["JUHE_AI_PROXY_LATENCY_BATCH_SIZE"] = "64"
	full["JUHE_AI_PROXY_LATENCY_INTERVAL"] = "10s"
	cfg, err = LoadRuntimeConfig(wfEnv(full))
	if err != nil {
		t.Fatalf("合法配置失败: %v", err)
	}
	if !cfg.Enabled || cfg.InstanceID != "wf-i" || cfg.Store.Mode != StorePostgres || cfg.PostgresMaxOpenConns != 8 || cfg.PostgresMaxIdleConns != 4 ||
		cfg.InputPostgresMaxOpenConns != 6 || cfg.InputPostgresMaxIdleConns != 3 || cfg.InputLimit != 100 || cfg.BatchSize != 64 || cfg.Interval != 10*time.Second {
		t.Fatalf("显式配置=%+v", cfg)
	}
	cfg, err = LoadRuntimeConfig(wfEnv(base))
	if err != nil {
		t.Fatalf("默认配置失败: %v", err)
	}
	if cfg.BatchSize != defaultProxyLatencyBatchSize || cfg.InputTTL != 5*time.Minute || cfg.OwnerLease != defaultProxyLatencyOwnerLease ||
		cfg.ProxyLease != defaultProxyLatencyProxyLease || cfg.ProbeTimeout != defaultProxyLatencyProbeTimeout ||
		cfg.CandidatePoolFactor != defaultProxyLatencyPoolFactor || cfg.WorkerConcurrency != defaultProxyLatencyConcurrency ||
		cfg.DBConcurrency != defaultProxyLatencyDBConcurrency || cfg.DBQueueSize != defaultProxyLatencyDBQueueSize {
		t.Fatalf("默认配置=%+v", cfg)
	}
}

func TestWFRuntimeValueParsers(t *testing.T) {
	fallback, err := runtimeDuration(wfEnv(nil), "X", time.Minute, time.Second, time.Hour)
	if err != nil || fallback != time.Minute {
		t.Fatalf("空值回退=%v err=%v", fallback, err)
	}
	if _, err := runtimeDuration(wfEnv(map[string]string{"X": "nope"}), "X", time.Minute, time.Second, time.Hour); err == nil {
		t.Fatal("非法 duration 必须报错")
	}
	value, err := runtimeInt(wfEnv(map[string]string{"X": "5"}), "X", 1, 1, 10)
	if err != nil || value != 5 {
		t.Fatalf("runtimeInt=%d err=%v", value, err)
	}
	if _, err := runtimeInt(wfEnv(map[string]string{"X": "0"}), "X", 1, 1, 10); err == nil {
		t.Fatal("越下界必须报错")
	}
	if _, err := runtimeInt(wfEnv(map[string]string{"X": "x"}), "X", 1, 1, 10); err == nil {
		t.Fatal("非数字必须报错")
	}
	if value, err := positiveInt(wfEnv(map[string]string{"X": "7"}), "X", 1); err != nil || value != 7 {
		t.Fatalf("positiveInt=%d err=%v", value, err)
	}
	if _, err := positiveInt(wfEnv(map[string]string{"X": "0"}), "X", 1); err == nil {
		t.Fatal("非正数必须报错")
	}
}

func TestWFDBConcurrencyGate(t *testing.T) {
	if NewDBConcurrencyGate(0, 10) != nil {
		t.Fatal("并发为 0 必须返回 nil 闸门")
	}
	gate := NewDBConcurrencyGate(2, 0)
	if gate.Capacity() != 2 {
		t.Fatalf("容量=%d", gate.Capacity())
	}
	release, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if gate.QueueDepth() != 1 {
		t.Fatalf("队列深度=%d", gate.QueueDepth())
	}
	release()
	if gate.QueueDepth() != 0 {
		t.Fatalf("释放后队列深度=%d", gate.QueueDepth())
	}

	// 队列满 + ctx 取消：立即失败且不占用槽位。
	tiny := NewDBConcurrencyGate(1, 1)
	first, _, err := tiny.Acquire(context.Background())
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := tiny.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("队列满取令牌 err=%v", err)
	}
	first()

	// nil 闸门全部为 no-op。
	var nilGate *DBConcurrencyGate
	release, _, err = nilGate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("nil 闸门 Acquire 失败: %v", err)
	}
	release()
	if nilGate.QueueDepth() != 0 || nilGate.Capacity() != 0 {
		t.Fatal("nil 闸门必须返回 0")
	}
}

// wfSealPassword 以 Node 兼容的 AES-256-GCM 信封加密密码（测试辅助）。
func wfSealPassword(t *testing.T, secret, password string) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("创建 cipher 失败: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("创建 GCM 失败: %v", err)
	}
	nonce := []byte("0123456789abcdef")[:12]
	sealed := gcm.Seal(nil, nonce, []byte(`{"password":"`+password+`"}`), nil)
	ciphertext, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	return "v1:" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(tag) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)
}

func TestWFDecryptProxyPasswordV1(t *testing.T) {
	secret := "wf-secret"
	envelope := wfSealPassword(t, secret, "pw-123")
	password, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: envelope})
	if err != nil || password != "pw-123" {
		t.Fatalf("解密=%q err=%v", password, err)
	}
	if _, err := decryptProxyPasswordV1("", CredentialEnvelope{Kind: "proxy_password", Ciphertext: envelope}); err == nil {
		t.Fatal("空密钥必须拒绝")
	}
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "other", Ciphertext: envelope}); err == nil {
		t.Fatal("错误 kind 必须拒绝")
	}
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:broken"}); err == nil {
		t.Fatal("损坏信封必须拒绝")
	}
	if _, err := decryptProxyPasswordV1("wrong-secret", CredentialEnvelope{Kind: "proxy_password", Ciphertext: envelope}); err == nil {
		t.Fatal("错误密钥必须解密失败")
	}
}

func TestWFProxyURLForIssuedInput(t *testing.T) {
	valid := func(mutate func(*IssuedInput)) IssuedInput {
		input := IssuedInput{
			ProxyID: "p-1", RequestID: "r-1", InputVersion: 1, ConfigRevision: wfProjRevision, Trigger: TriggerPeriodic,
			IssuedAt: wfProjBase.UTC(), ExpiresAt: wfProjBase.Add(5 * time.Minute).UTC(), PolicyVersion: proxyLatencyInputPolicyVersion,
			ProxyType: "http", ProxyHost: "10.0.0.1", ProxyPort: 8080,
			Targets: []Target{{Provider: "gpt", ProfileID: "profile-gpt", URL: "https://api.openai.com/v1"}},
		}
		mutate(&input)
		return input
	}
	proxyURL, err := proxyURLForIssuedInput(valid(func(*IssuedInput) {}), "secret")
	if err != nil || !strings.HasPrefix(proxyURL, "http://10.0.0.1:8080") {
		t.Fatalf("无凭据 URL=%q err=%v", proxyURL, err)
	}
	proxyURL, err = proxyURLForIssuedInput(valid(func(input *IssuedInput) {
		input.ProxyUsername = "user"
	}), "secret")
	if err != nil || !strings.Contains(proxyURL, "user@") {
		t.Fatalf("仅用户名 URL=%q err=%v", proxyURL, err)
	}
	proxyURL, err = proxyURLForIssuedInput(valid(func(input *IssuedInput) {
		input.ProxyUsername = "user"
		input.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: wfSealPassword(t, "secret", "pw")}
	}), "secret")
	if err != nil || !strings.Contains(proxyURL, "user:pw@") {
		t.Fatalf("带凭据 URL=%q err=%v", proxyURL, err)
	}
	if _, err := proxyURLForIssuedInput(valid(func(input *IssuedInput) { input.ProxyType = "ftp" }), "secret"); err == nil {
		t.Fatal("非法类型必须拒绝")
	}
	if _, err := proxyURLForIssuedInput(valid(func(input *IssuedInput) { input.ProxyHost = " " }), "secret"); err == nil {
		t.Fatal("空主机必须拒绝")
	}
	if _, err := proxyURLForIssuedInput(valid(func(input *IssuedInput) { input.ProxyPort = 0 }), "secret"); err == nil {
		t.Fatal("端口 0 必须拒绝")
	}
	if _, err := proxyURLForIssuedInput(valid(func(input *IssuedInput) {
		input.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: wfSealPassword(t, "secret", "pw")}
	}), "secret"); err == nil {
		t.Fatal("password 缺 username 必须拒绝")
	}
	if _, err := proxyURLForIssuedInput(valid(func(input *IssuedInput) {
		input.ProxyUsername = "user"
		input.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: "v1:broken"}
	}), "secret"); err == nil {
		t.Fatal("坏信封必须拒绝")
	}
}

// wfTimeoutNetError 实现 net.Error 且 Timeout()=true 的最小时钟错误。
type wfTimeoutNetError struct{}

func (wfTimeoutNetError) Error() string   { return "i/o timeout" }
func (wfTimeoutNetError) Timeout() bool   { return true }
func (wfTimeoutNetError) Temporary() bool { return true }

func TestWFTransportFailureCodes(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	if code := responseReadFailureResult(timeoutErr).ErrorCode; code != "timeout" {
		t.Fatalf("超时码=%q", code)
	}
	timeoutNetErr := wfTimeoutNetError{}
	if code := responseReadFailureResult(timeoutNetErr).ErrorCode; code != "timeout" {
		t.Fatalf("网络超时码=%q", code)
	}
	if code := responseReadFailureResult(io.ErrUnexpectedEOF).ErrorCode; code != "early_eof" {
		t.Fatalf("提前结束码=%q", code)
	}
	if code := proxyConfigurationCode(errors.New("proxy required")); code != "proxy_required" {
		t.Fatalf("缺代理码=%q", code)
	}
	if code := proxyConfigurationCode(errors.New("proxy URL invalid")); code != "proxy_invalid" {
		t.Fatalf("非法代理码=%q", code)
	}
	if transportFailureResult(context.DeadlineExceeded).ErrorCode != "timeout" {
		t.Fatal("transport 超时码错误")
	}
	dnsErr := &net.DNSError{Err: "nx", Name: "x.invalid", IsNotFound: true}
	if transportFailureResult(dnsErr).ErrorCode != "dns" {
		t.Fatal("dns 码错误")
	}
}

func TestWFTargetHostAndPort(t *testing.T) {
	for _, host := range []string{"api.openai.com", "API.OpenAI.com", "127.0.0.1", "::1", "api.openai.com."} {
		if !validTargetHostname(host) {
			t.Fatalf("%q 必须合法", host)
		}
	}
	for _, host := range []string{"", ".openai.com", "a..b", "http://x", "a_b.c", "-a.b", "a-.b", strings.Repeat("a", 254) + ".com"} {
		if validTargetHostname(host) {
			t.Fatalf("%q 必须非法", host)
		}
	}
	port, err := canonicalTargetPort("api.openai.com")
	if err != nil || port != "" {
		t.Fatalf("无端口=%q err=%v", port, err)
	}
	if port, err = canonicalTargetPort("host:8080"); err != nil || port != "8080" {
		t.Fatalf("带端口=%q err=%v", port, err)
	}
	if port, err = canonicalTargetPort("[::1]"); err != nil || port != "" {
		t.Fatalf("裸 IPv6=%q err=%v", port, err)
	}
	if port, err = canonicalTargetPort("[::1]:443"); err != nil || port != "443" {
		t.Fatalf("IPv6 带端口=%q err=%v", port, err)
	}
	for _, bad := range []string{"[::1", "[::1]x", "[::1]:", "a:b:c", "host:", ":8080", "host:0", "host:99999"} {
		if _, err := canonicalTargetPort(bad); err == nil {
			t.Fatalf("%q 必须报错", bad)
		}
	}
}
