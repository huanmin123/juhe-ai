// w14d_unit_tails_test.go covers the pure/defensive tails that the HTTP
// lifecycle cannot reach: bearer parse edge, last-used touch guard, rate-limit
// JSON tolerance, zod null handling, capture body read failure and the redis
// driver clock fallback.
//
// w14d 不可达/未覆盖语句登记（go test ./internal/aipublic/ -cover，截止 w14d
// 共 134 条未覆盖，均为防御分支或需特定故障注入的组合，已按当前边界判定为
// HTTP 层不可达或低价值 mock 注入，不做删除）：
//   - accounts.go：update/delete 的 FindEditBasicDetail/Delete 错误臂与 CAS
//     重试臂（800-821、896-912、1118-1147，需在单请求中途令 store 失败）；
//     ensureTargetSystemAccount/ensureTargetGroup/create 失败臂（475、555、570、
//     578，accounts 表删除后 ensureTargetGroup 的分组详情查询先失败）；notes
//     超长（742，与 update 已覆盖的另一字段臂同构）；resolveAccountGroupFilter
//     的空 profile/查询错误臂（959、969、981，见上）；readAccountGroupItem 空
//     结果臂（1000、1003，账号已验证属于该组）；模型 scan 错误臂（276）；更新
//     后 summary 分组兜底臂（927、930，见上）；add targetDisplayName 缺失臂
//     （315，语义为"空串视为未提供"）；delete targetUsername 校验臂（1089 已
//     覆盖，此处为同构变体）。
//   - apikeys.go：expiresAt 校验臂（185，requiredTrimmedBody 0..0 语义为不
//     限长）；Create 后 FindDetail 错误臂（247）；FindEditBasicDetail/Delete
//     错误臂与 ValidationCacheError 组合（380-434、487-508，需配额缓存失败
//     注入）。
//   - groups.go/strategies.go：store FindDetail/Update/Delete 错误臂与
//     scan/rows.Err（155、166、308、323-331、449-491、543-560、120、212-231、
//     421、607-672、698-741，需在单请求中途令 store 失败）；列表页码/用户名
//     解析臂（44、64 已覆盖变体）。
//   - target.go：ensureTargetSystemAccount 随机口令/建号失败臂（88-137）、
//     resolveOwnedTarget 查询错误臂（199-205）、requireProviderProfile 空
//     profileId（265 已由 raw 账号覆盖，此处为 profile 查询错误臂）、
//     randomSecret rand 失败臂（405）。
//   - zod.go:319（trimmed==nil 且无 issue 的组合）、operationlog.go:120/129
//     （json.Marshal 对已解码 JSON 值不会失败）、ratelimit.go:336（被阻塞
//     条目在 trim 中跳过——已由 TestW14dPenaltyLimiterTrim 覆盖前半）。
package aipublic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestW14dBearerTokenBlankValue(t *testing.T) {
	// A bearer value that trims to empty yields an empty token (L154): a
	// non-breaking space matches (.+) but TrimSpace removes it.
	request := httptest.NewRequest(http.MethodGet, "/__aipublic__/group/list", nil)
	request.Header.Set("Authorization", "Bearer\u00a0")
	if token := bearerToken(request); token != "" {
		t.Fatalf("blank bearer value: %q", token)
	}
}

func TestW14dTouchLastUsedSkipsInvalidClock(t *testing.T) {
	// An unparsable now text skips the touch updates entirely (auth L175).
	deps := &Deps{}
	deps.touchLastUsed(context.Background(), &tokenAuthRow{}, "not-an-rfc3339-instant")
}

func TestW14dDecodeRateLimitsToleratesNonObjects(t *testing.T) {
	// Non-object entries inside rate_limits_json are skipped (auth L248).
	rules := decodeRateLimitsList(`[1, "x", {"windowSeconds":60,"maxRequests":2}]`)
	if len(rules) != 1 || rules[0].WindowSeconds != 60 || rules[0].MaxRequests != 2 {
		t.Fatalf("rules: %+v", rules)
	}
}

func TestW14dRequiredTrimmedBodyNullValue(t *testing.T) {
	// A present-but-null required field renders the zod required issue
	// (zod L319).
	_, issue := requiredTrimmedBody(map[string]any{"k": nil}, "k", 1, 10)
	if issue == "" {
		t.Fatal("null required field must produce an issue")
	}
}

func TestW14dBufferCaptureBodyReadFailure(t *testing.T) {
	// A body reader that fails mid-read marks the capture as rejected
	// (capture L116).
	request := httptest.NewRequest(http.MethodPost, "/__aipublic__/group/add", errReader{})
	buffered := bufferCaptureRequestBody(request)
	if buffered == nil || !buffered.parseFailed {
		t.Fatalf("buffered: %+v", buffered)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestW14dRedisDriverClockFallback(t *testing.T) {
	// Without an injected clock the redis driver falls back to time.Now
	// (ratelimit L60).
	deps := &Deps{}
	if driver := deps.redisDriver(); driver == nil {
		t.Fatal("redis driver must build lazily")
	}
}

func TestW14dPenaltyLimiterTrim(t *testing.T) {
	// trim drops the oldest last-seen buckets beyond maxEntries, keeping
	// actively blocked ones (ratelimit L330/L336).
	limiter := NewPenaltyWindowLimiter(nil)
	limiter.maxEntries = 3
	limiter.entries["a"] = &penaltyEntry{lastSeenAtMs: 100}
	limiter.entries["b"] = &penaltyEntry{lastSeenAtMs: 300, hasBlock: true, blockedUntilMs: 999_999}
	limiter.entries["c"] = &penaltyEntry{lastSeenAtMs: 200}
	limiter.entries["d"] = &penaltyEntry{lastSeenAtMs: 400}
	limiter.entries["e"] = &penaltyEntry{lastSeenAtMs: 500}
	limiter.trim(1000)
	if _, ok := limiter.entries["a"]; ok {
		t.Fatal("oldest entry must be trimmed")
	}
	if _, ok := limiter.entries["b"]; !ok {
		t.Fatal("actively blocked entry must survive the trim")
	}
	if len(limiter.entries) != 3 {
		t.Fatalf("entries after trim: %d", len(limiter.entries))
	}
}
