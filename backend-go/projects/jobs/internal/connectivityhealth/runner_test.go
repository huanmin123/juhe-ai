package connectivityhealth

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// TestCandidateSQLOnlyConnectionTestAccounts 锁定候选口径：媒体域与向量/
// 重排域全部 + chat 域只声明非对话形态的账户；J1 的对话生成形态不得出现
// 在本通道，本通道也不得再出现图像真实生图形态。
func TestCandidateSQLOnlyConnectionTestAccounts(t *testing.T) {
	if !strings.Contains(connectivityCandidateSQL, "a.capability_domain IN ('video', 'audio', 'image', 'embedding', 'rerank')") {
		t.Fatal("候选 SQL 必须覆盖全部媒体能力域与向量/重排域")
	}
	if !strings.Contains(connectivityCandidateSQL, "AND NOT EXISTS") {
		t.Fatal("chat 域必须排除含对话生成形态的账户")
	}
	if !strings.Contains(connectivityCandidateSQL, "'interactions_sse'") {
		t.Fatal("对话生成形态排除集必须覆盖全部 10 个模型测试形态")
	}
	if strings.Contains(connectivityCandidateSQL, "images_json") {
		t.Fatal("连接测试通道不得出现图像生图形态")
	}
	if !strings.Contains(connectivityCandidateSQL, "a.background_probe_enabled = 1") {
		t.Fatal("候选 SQL 必须尊重后台自动探测总开关")
	}
	if !strings.Contains(connectivityCandidateSQL, "LIMIT $2") {
		t.Fatal("候选 SQL 必须保持批量上限")
	}
	// 授权实例没有自身连接行（资源事实归来源账户），chat 域的 EXISTS /
	// NOT EXISTS 都必须按资源账户 ID 查连接，否则实例永不入选。
	const resourceConnectionAccount = "c.account_id = CASE WHEN a.authorization_instance_authorization_id IS NULL\n" +
		"            THEN a.id ELSE a.authorization_instance_source_account_id END"
	if got := strings.Count(connectivityCandidateSQL, resourceConnectionAccount); got != 2 {
		t.Fatalf("chat 域 EXISTS/NOT EXISTS 必须各有一次资源账户 CASE 连接归属，got %d:\n%s",
			got, connectivityCandidateSQL)
	}
	if strings.Contains(connectivityCandidateSQL, "c.account_id = a.id") {
		t.Fatal("候选 SQL 不得再用实例自身 id 查连接（授权实例无自身连接行）")
	}
}

func TestClampConnectivityBudget(t *testing.T) {
	cases := []struct {
		batch, concurrency, wantBatch, wantConcurrency int
	}{
		{50, 512, 50, 512},
		{50, 4, 4, 4},
		{4, 4, 4, 4},
		{512, 512, 512, 512},
		{600, 512, 512, 512},
	}
	for _, test := range cases {
		batch, concurrency := clampConnectivityBudget(test.batch, test.concurrency)
		if batch != test.wantBatch || concurrency != test.wantConcurrency {
			t.Fatalf("clampConnectivityBudget(%d,%d) = (%d,%d), want (%d,%d)",
				test.batch, test.concurrency, batch, concurrency, test.wantBatch, test.wantConcurrency)
		}
	}
	// 默认预算经收敛后的最坏一轮是「全部候选同上游」：同上游并发只有
	// perUpstreamConcurrency，墙钟 = ceil(batch/perUpstream)*probeTimeout，
	// 必须小于 job 调度 15 分钟 timeout（LeaseTTL 同步 15 分钟）；预算余量还
	// 要覆盖每波收尾的 DB 写与状态版本 bump（≤3s/波）。
	effectiveBatch, effectiveConcurrency := clampConnectivityBudget(maxBatchSize, maxConcurrency)
	waves := (effectiveBatch + perUpstreamConcurrency - 1) / perUpstreamConcurrency
	if waves != 9 || effectiveConcurrency != 512 {
		t.Fatalf("上限档应为 512 并发 / 9 波，got (%d, %d 波)", effectiveConcurrency, waves)
	}
	if worst := probeTimeout * time.Duration(waves); worst >= 15*time.Minute {
		t.Fatalf("默认预算最坏耗时 %s 必须小于 job 调度 timeout", worst)
	}
	if worstWithWrite := probeTimeout*time.Duration(waves) + 3*time.Second*time.Duration(waves); worstWithWrite >= 15*time.Minute {
		t.Fatalf("含写库开销的最坏耗时 %s 必须小于 job 调度 15 分钟 timeout", worstWithWrite)
	}
}

// TestUpstreamOrigin 锁定同上游限流的分组键：scheme://host:port 归一，解析
// 失败回落原始字符串，空值共享空键（保守限流，不放大瞬时压力）。
func TestUpstreamOrigin(t *testing.T) {
	cases := []struct {
		baseURL, want string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com"},
		{"https://api.openai.com:8443/v1", "https://api.openai.com:8443"},
		{"  https://dashscope.aliyuncs.com  ", "https://dashscope.aliyuncs.com"},
		{"http://api.qwen.com", "http://api.qwen.com"},
		{"https://api.openai.com/v1#fragment", "https://api.openai.com"},
		// 非法 URL 回落原串：同一非法值恒映射同一键。
		{"://bad url", "://bad url"},
		{"not-a-url", "not-a-url"},
		{"", ""},
	}
	for _, test := range cases {
		if got := upstreamOrigin(test.baseURL); got != test.want {
			t.Fatalf("upstreamOrigin(%q) = %q, want %q", test.baseURL, got, test.want)
		}
	}
}

// TestUpstreamLimiterBucketIsolation 锁定同上游限流桶语义：同 origin 共享
// 容量 perUpstreamConcurrency 的桶，不同 origin 各自独立（互不占额）。
func TestUpstreamLimiterBucketIsolation(t *testing.T) {
	limiter := newUpstreamLimiter()
	a := limiter.bucket("https://api.example.com")
	a2 := limiter.bucket("https://api.example.com")
	b := limiter.bucket("https://other.example.com")
	if a != a2 {
		t.Fatal("同 origin 必须共享同一个限流桶")
	}
	if a == b {
		t.Fatal("不同 origin 必须使用独立限流桶")
	}
	// 桶容量 = perUpstreamConcurrency：塞满后再次获取阻塞，释放后可复用。
	for i := 0; i < perUpstreamConcurrency; i++ {
		a <- struct{}{}
	}
	full := limiter.bucket("https://api.example.com")
	select {
	case full <- struct{}{}:
		t.Fatal("同源桶塞满后不得继续放行")
	default:
	}
	<-a
	select {
	case full <- struct{}{}:
	default:
		t.Fatal("释放一个名额后应可再次获取")
	}
}

func TestUsesConnectionTest(t *testing.T) {
	cases := []struct {
		domain string
		modes  []string
		want   bool
	}{
		{"image", []string{"images_json"}, true},
		{"audio", []string{"audio_speech"}, true},
		{"video", []string{"video_create", "video_get"}, true},
		// 五域退役修订（AI 账户上游接口能力设计 §5）：五域恒连接测试，不看
		// modes——revision 30 清洗后声明集为空仍按域入选。
		{"image", nil, true},
		{"audio", nil, true},
		{"video", nil, true},
		{"embedding", nil, true},
		{"rerank", nil, true},
		// 五域即使残留旧声明词面也按域判定（不猜 modes）。
		{"video", []string{"chat_json"}, true},
		{"chat", []string{"embeddings_json", "rerank_json"}, true},
		{"chat", []string{"embed_content"}, true},
		{"chat", []string{"count_tokens"}, true},
		{"chat", nil, true},
		{"chat", []string{"chat_json"}, false},
		{"chat", []string{"embeddings_json", "chat_sse"}, false},
		{"chat", []string{"responses_sse"}, false},
		{"chat", []string{"messages_json"}, false},
		{"chat", []string{"generate_content_sse"}, false},
		{"chat", []string{"interactions_json"}, false},
	}
	for _, tc := range cases {
		if got := usesConnectionTest(tc.domain, tc.modes); got != tc.want {
			t.Fatalf("usesConnectionTest(%q, %v) = %v, want %v", tc.domain, tc.modes, got, tc.want)
		}
	}
}

func TestBuildHealthUpdateSuccessRestoresPendingTest(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	update := healthUpdate{
		now:              now,
		nextDue:          now.Add(24 * time.Hour),
		statusCode:       200,
		failureCount:     0,
		currentStatus:    "pending_test",
		configRevision:   7,
		dispatchRevision: 3,
		success:          true,
		restore:          true,
	}
	query, params := buildHealthUpdate("acct-1", update)
	if !strings.Contains(query, "last_health_success_at = $10") {
		t.Fatalf("成功必须写最后成功时间: %s", query)
	}
	if !strings.Contains(query, "status = $11") || !strings.Contains(query, "schedulable = 1") {
		t.Fatalf("待测试账户成功必须恢复 active/schedulable: %s", query)
	}
	if strings.Contains(query, "cooldown_until = $") {
		t.Fatalf("成功恢复不得写冷却时间: %s", query)
	}
	if len(params) != 15 {
		t.Fatalf("params len = %d, want 15: %#v", len(params), params)
	}
	if params[10] != "active" {
		t.Fatalf("status param = %#v, want active", params[10])
	}
	if params[11] != "acct-1" || params[12] != "pending_test" ||
		params[13] != int64(7) || params[14] != int64(3) {
		t.Fatalf("CAS 参数错误: %#v", params[11:])
	}
	if !strings.Contains(query, "AND status = $13") ||
		!strings.Contains(query, "config_revision = $14") ||
		!strings.Contains(query, "dispatch_revision = $15") {
		t.Fatalf("CAS guard 占位符错误: %s", query)
	}
}

func TestBuildHealthUpdateSuccessWithoutRestoreKeepsStatus(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	query, params := buildHealthUpdate("acct-2", healthUpdate{
		now: now, nextDue: now.Add(24 * time.Hour), statusCode: 200,
		currentStatus: "active", configRevision: 1, dispatchRevision: 1,
		success: true,
	})
	if setsContain(query, "status = $") || setsContain(query, "schedulable =") {
		t.Fatalf("已 active 账户成功探测不得重写调度状态: %s", query)
	}
	if len(params) != 14 || params[10] != "acct-2" {
		// base 9 + last_health_success_at 1 = 10，再加 4 个 CAS 参数。
		t.Fatalf("params = %#v", params)
	}
}

// TestBuildHealthUpdateAuthFailureNeverPasses 锁定 401/403 语义：写 error、
// 不可调度、不写冷却，错误码固定为 connectivity_auth_failed 以支持后续
// 成功探测恢复。
func TestBuildHealthUpdateAuthFailureNeverPasses(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	query, params := buildHealthUpdate("acct-3", healthUpdate{
		now: now, nextDue: now.Add(24 * time.Hour), statusCode: 401,
		errorCode: "invalid_api_key", errorMessage: "密钥无效",
		currentStatus: "active", configRevision: 2, dispatchRevision: 5,
		authFailure: true,
	})
	if !strings.Contains(query, "status = $10") || !strings.Contains(query, "schedulable = 0") {
		t.Fatalf("认证失败必须落 error 且不可调度: %s", query)
	}
	if !strings.Contains(query, "cooldown_until = NULL") {
		t.Fatalf("认证失败不得写冷却: %s", query)
	}
	if !strings.Contains(query, "last_error_code = $11") {
		t.Fatalf("认证失败必须写稳定错误码: %s", query)
	}
	if strings.Contains(query, "last_health_success_at") {
		t.Fatalf("认证失败不得写成功时间: %s", query)
	}
	if len(params) != 16 {
		t.Fatalf("params len = %d, want 16: %#v", len(params), params)
	}
	if params[9] != "error" || params[10] != "connectivity_auth_failed" || params[11] != "密钥无效" {
		t.Fatalf("认证失败状态/错误码参数错误: %#v", params[9:12])
	}
	if params[12] != "acct-3" || params[13] != "active" ||
		params[14] != int64(2) || params[15] != int64(5) {
		t.Fatalf("CAS 参数错误: %#v", params[12:])
	}
}

func TestBuildHealthUpdateTransientKeepsStatus(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	query, params := buildHealthUpdate("acct-4", healthUpdate{
		now: now, nextDue: now.Add(5 * time.Minute), errorCode: "connectivity_upstream_unavailable",
		currentStatus: "active", configRevision: 1, dispatchRevision: 1, transient: true,
	})
	if setsContain(query, "status = $") || setsContain(query, "schedulable =") ||
		setsContain(query, "cooldown_until") {
		t.Fatalf("网络/5xx 只记录健康事实，不得改调度状态: %s", query)
	}
	if params[3] != "connectivity_upstream_unavailable" {
		t.Fatalf("健康事实错误码未写入: %#v", params[3])
	}
	if len(params) != 13 || params[9] != "acct-4" || params[10] != "active" {
		t.Fatalf("CAS 参数错误: %#v", params)
	}
}

// TestBuildHealthUpdateMissingProbeSpecAdvancesDue 锁定无探测规格的退避事实：
// 必须写 last_health_check_at/next_health_check_at 与稳定错误码，避免
// pending_test 每 tick 被重新选中。
func TestBuildHealthUpdateMissingProbeSpecAdvancesDue(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	query, params := buildHealthUpdate("acct-missing-spec", healthUpdate{
		now: now, nextDue: now.Add(5 * time.Minute),
		errorCode: "connectivity_probe_spec_missing", errorMessage: "账户没有可用于连接测试的请求形态",
		currentStatus: "pending_test", configRevision: 1, dispatchRevision: 1, transient: true,
	})
	if !strings.Contains(query, "last_health_check_at = $1") || !strings.Contains(query, "next_health_check_at = $2") {
		t.Fatalf("无规格必须推进健康检查时间: %s", query)
	}
	if params[3] != "connectivity_probe_spec_missing" {
		t.Fatalf("错误码 = %#v", params[3])
	}
	if setsContain(query, "status = $") {
		t.Fatalf("无规格不得改调度状态: %s", query)
	}
}

func TestNextHealthCheckAt(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	schedule := healthSchedule{Interval: 24 * time.Hour}
	if got := nextHealthCheckAt(now, schedule, true, false); !got.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("成功探测间隔 = %v, want 24h", got.Sub(now))
	}
	if got := nextHealthCheckAt(now, schedule, false, false); !got.Equal(now.Add(transientRetryDelay)) {
		t.Fatalf("瞬态失败复测 = %v, want %v", got.Sub(now), transientRetryDelay)
	}
	if got := nextHealthCheckAt(now, schedule, false, true); !got.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("认证失败按正常周期复测 = %v, want 24h", got.Sub(now))
	}
	withJitter := nextHealthCheckAt(now, healthSchedule{Interval: 24 * time.Hour, Jitter: 10 * time.Minute}, true, false)
	delta := withJitter.Sub(now.Add(24 * time.Hour))
	if delta < -5*time.Minute || delta > 5*time.Minute {
		t.Fatalf("抖动必须落在 ±5m: %v", delta)
	}
	// 瞬态失败不得被 jitter 放大：无论配置多大抖动都固定 5 分钟复测。
	transientWithJitter := nextHealthCheckAt(now, healthSchedule{Interval: 24 * time.Hour, Jitter: 24 * time.Hour}, false, false)
	if !transientWithJitter.Equal(now.Add(transientRetryDelay)) {
		t.Fatalf("瞬态失败复测被 jitter 放大：%v, want %v", transientWithJitter.Sub(now), transientRetryDelay)
	}
}

func TestNextFailureState(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	count, startedAt := nextFailureState(candidate{FailureCount: 2}, now, true)
	if count != 0 || startedAt != nil {
		t.Fatalf("成功必须清零失败状态: %d %#v", count, startedAt)
	}
	count, startedAt = nextFailureState(candidate{FailureCount: 2}, now, false)
	if count != 3 {
		t.Fatalf("失败计数 = %d, want 3", count)
	}
	if text, ok := startedAt.(string); !ok || text != now.Format(time.RFC3339Nano) {
		t.Fatalf("首次失败起始时间 = %#v", startedAt)
	}
	count, startedAt = nextFailureState(candidate{
		FailureCount:     4,
		FailureStartedAt: sql.NullString{String: "2026-11-20T10:00:00Z", Valid: true},
	}, now, false)
	if count != 5 {
		t.Fatalf("失败计数 = %d, want 5", count)
	}
	if text, ok := startedAt.(string); !ok || text != "2026-11-20T10:00:00Z" {
		t.Fatalf("既有失败起始时间必须保留: %#v", startedAt)
	}
}

func TestBoundedMessage(t *testing.T) {
	if got := boundedMessage("  短消息  "); got != "短消息" {
		t.Fatalf("boundedMessage = %q", got)
	}
	long := strings.Repeat("汉", 1005)
	got := boundedMessage(long)
	if len([]rune(got)) != 1001 || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长消息必须按 rune 截断: %d", len([]rune(got)))
	}
}

func TestIsConnectivityAuthError(t *testing.T) {
	if !isConnectivityAuthError(" connectivity_auth_failed ") {
		t.Fatal("连接认证失败码必须可识别")
	}
	if isConnectivityAuthError("invalid_api_key") {
		t.Fatal("其他错误码不得视为连接认证失败")
	}
}

// setsContain 只在 UPDATE ... SET 段内检查，避免把 CAS guard 的
// `AND status = $n` 误判为状态写入。
func setsContain(query, fragment string) bool {
	index := strings.Index(query, " WHERE ")
	if index < 0 {
		return false
	}
	return strings.Contains(query[:index], fragment)
}
