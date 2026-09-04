package gatewayclientip

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func newTestCircuit(t *testing.T, mutate func(*ErrorCircuitOptions)) (*ErrorCircuit, *manualClock) {
	t.Helper()
	clock := newManualClock(time.UnixMilli(1_000_000))
	opts := ErrorCircuitOptions{Clock: clock, RuntimeStateDriver: RuntimeStateDriverMemory}
	if mutate != nil {
		mutate(&opts)
	}
	circuit, err := NewErrorCircuit(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)
	return circuit, clock
}

func TestPreAuthSpecificKeyTable(t *testing.T) {
	circuit, _ := newTestCircuit(t, nil)
	tests := []struct {
		name string
		ip   string
		auth string
		want string
		ok   bool
	}{
		{name: "blank ip", ip: "  ", want: "", ok: false},
		{name: "missing token", ip: "10.0.0.1", auth: "", want: "preauth:10.0.0.1:missing", ok: true},
		{name: "non bearer", ip: "10.0.0.1", auth: "Basic abc", want: "preauth:10.0.0.1:missing", ok: true},
		{name: "bearer fingerprint", ip: "10.0.0.1", auth: "Bearer abc", want: "preauth:10.0.0.1:token:" + tokenFingerprint("abc"), ok: true},
		{name: "case insensitive bearer", ip: "10.0.0.1", auth: "bEaReR   abc  ", want: "preauth:10.0.0.1:token:" + tokenFingerprint("abc"), ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := circuit.preAuthSpecificKey(gatewaypreauth.PreAuthCircuitInput{ClientIP: tc.ip, Authorization: tc.auth})
			if ok != tc.ok || key != tc.want {
				t.Fatalf("key=%q ok=%v want %q %v", key, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestPreAuthCircuitThresholdsTable(t *testing.T) {
	tests := []struct {
		name      string
		reason    gatewaypreauth.PreAuthFailureReason
		threshold int64
	}{
		{name: "missing bearer", reason: gatewaypreauth.PreAuthFailureMissingBearerToken, threshold: preAuthMissingThreshold},
		{name: "invalid api key", reason: gatewaypreauth.PreAuthFailureInvalidAPIKey, threshold: preAuthInvalidTokenThreshold},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			circuit, _ := newTestCircuit(t, nil)
			input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.1", Authorization: "Bearer tok", Reason: tc.reason}
			var decision gatewaypreauth.CircuitDecision
			for i := int64(1); i <= tc.threshold; i += 1 {
				decision = circuit.RecordGatewayPreAuthFailure(input)
				if i < tc.threshold && decision.Blocked {
					t.Fatalf("第 %d 次不应熔断: %+v", i, decision)
				}
			}
			if !decision.Blocked {
				t.Fatalf("达到阈值必须熔断: %+v", decision)
			}
			if decision.Reason != string(tc.reason) {
				t.Fatalf("reason=%q", decision.Reason)
			}
			if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds != 30 {
				t.Fatalf("retryAfter=%v want 30", decision.RetryAfterSeconds)
			}
			if decision.FailureCount == nil || *decision.FailureCount != tc.threshold {
				t.Fatalf("failureCount=%v", decision.FailureCount)
			}
			// 熔断期间继续记录 → 直接返回当前 decision，窗口不增长。
			next := circuit.RecordGatewayPreAuthFailure(input)
			if !next.Blocked || next.BlockedUntilMs != decision.BlockedUntilMs {
				t.Fatalf("blocked during block: %+v", next)
			}
			// 检查也返回 blocked。
			inspect := circuit.InspectGatewayPreAuthCircuit(gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1", Authorization: "Bearer tok"})
			if !inspect.Blocked {
				t.Fatal("inspect must report blocked")
			}
		})
	}
}

func TestPreAuthCircuitBlockExponentialAndRecovery(t *testing.T) {
	circuit, clock := newTestCircuit(t, nil)
	input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.1", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}

	block := func() gatewaypreauth.CircuitDecision {
		for {
			decision := circuit.RecordGatewayPreAuthFailure(input)
			if decision.Blocked {
				return decision
			}
			if *decision.FailureCount > preAuthMissingThreshold {
				t.Fatal("window should cap samples")
			}
			// 推进一小步让窗口保留样本（60s 窗口内连击）。
			clock.advance(time.Millisecond)
		}
	}
	first := block()
	firstUntil := *first.BlockedUntilMs - clock.Now().UnixMilli()
	if firstUntil != preAuthInitialBlockMs {
		t.Fatalf("first block=%dms want %d", firstUntil, preAuthInitialBlockMs)
	}
	// 第一次 block 结束后（还在 60s 窗口内？window 从最后一次样本算），立刻再打满 → 第二次 block 60s。
	clock.advance(31 * time.Second)
	second := block()
	secondUntil := *second.BlockedUntilMs - clock.Now().UnixMilli()
	if secondUntil != 60_000 {
		t.Fatalf("second block=%dms want 60000 (2^1)", secondUntil)
	}
	// 恢复：推进到 block 结束 → 未熔断（样本被 60s 窗口逐步清理）。
	clock.advance(61 * time.Second)
	inspect := circuit.InspectGatewayPreAuthCircuit(gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1"})
	if inspect.Blocked {
		t.Fatalf("block 必须到期恢复: %+v", inspect)
	}
	if inspect.FailureCount == nil {
		t.Fatal("unblocked decision keeps failureCount")
	}
}

func TestPreAuthCircuitInvalidTokenSpray(t *testing.T) {
	circuit, _ := newTestCircuit(t, nil)
	// 每个 token 8 次内不触发 specific（threshold=8），但 spray 累计到 120 触发。
	var decision gatewaypreauth.CircuitDecision
	sprayHits := 0
	for token := 0; token < 20 && (decision == gatewaypreauth.CircuitDecision{} || !decision.Blocked); token += 1 {
		tokenText := "tok" + strconv.Itoa(token)
		for i := 0; i < preAuthInvalidTokenThreshold-1; i += 1 {
			decision = circuit.RecordGatewayPreAuthFailure(gatewaypreauth.PreAuthFailureInput{
				ClientIP: "10.0.0.1", Authorization: "Bearer " + tokenText,
				Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
			})
			sprayHits += 1
			if decision.Blocked {
				break
			}
		}
		if decision.Blocked {
			break
		}
	}
	if !decision.Blocked {
		t.Fatal("spray 阈值必须触发")
	}
	if decision.Reason != preAuthReasonInvalidTokenSpray {
		t.Fatalf("reason=%q want %q", decision.Reason, preAuthReasonInvalidTokenSpray)
	}
	// specific key 仍未熔断（每个 token 只打了 7 次）。
	specific := circuit.InspectGatewayPreAuthCircuit(gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1", Authorization: "Bearer tok0"})
	if specific.Blocked {
		t.Fatalf("specific 不应熔断: %+v", specific)
	}
	// spray 熔断中，新的 invalid_api_key 直接返回 spray decision。
	next := circuit.RecordGatewayPreAuthFailure(gatewaypreauth.PreAuthFailureInput{
		ClientIP: "10.0.0.1", Authorization: "Bearer fresh", Reason: gatewaypreauth.PreAuthFailureInvalidAPIKey,
	})
	if !next.Blocked || next.Reason != preAuthReasonInvalidTokenSpray {
		t.Fatalf("spray 阻断中的新失败必须直接返回: %+v", next)
	}
	_ = sprayHits
}

func TestClientIPErrorCircuitSignatureAndTotalThresholds(t *testing.T) {
	circuit, _ := newTestCircuit(t, nil)
	scope := gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", GroupID: "grp", APIKeyID: "key", ClientIP: "10.0.0.1", Endpoint: "/v1/chat/completions"}
	sample := func(reason gatewaypreauth.ClientIPErrorCircuitReason, signature string) gatewaypreauth.ClientIPErrorCircuitSampleInput {
		return gatewaypreauth.ClientIPErrorCircuitSampleInput{
			SystemAccountID: scope.SystemAccountID, GroupID: scope.GroupID, APIKeyID: scope.APIKeyID,
			ClientIP: scope.ClientIP, Endpoint: scope.Endpoint, Reason: reason, Signature: signature,
		}
	}
	// signature threshold = 5。
	for i := 0; i < clientIPSignatureThreshold-1; i += 1 {
		decision := circuit.RecordClientIPErrorCircuitSampleSync(sample("invalid_json", "sig-a"))
		if decision.Blocked {
			t.Fatalf("第 %d 次不应熔断: %+v", i+1, decision)
		}
	}
	decision := circuit.RecordClientIPErrorCircuitSampleSync(sample("invalid_json", "sig-a"))
	if !decision.Blocked || decision.Reason != circuitReasonInvalidJSON {
		t.Fatalf("signature 阈值必须熔断: %+v", decision)
	}
	// success 清除（sync）。
	if !circuit.RecordClientIPErrorCircuitSuccessSync(scope) {
		t.Fatal("success must report existing entry")
	}
	if circuit.RecordClientIPErrorCircuitSuccessSync(scope) {
		t.Fatal("second success must report false")
	}
	// total threshold = 20 个不同 signature。
	for i := 0; i < clientIPTotalThreshold-1; i += 1 {
		decision = circuit.RecordClientIPErrorCircuitSampleSync(sample("adapter_request_validation", "sig-"+strconv.Itoa(i)))
		if decision.Blocked {
			t.Fatalf("total=%d 不应熔断: %+v", i+1, decision)
		}
	}
	decision = circuit.RecordClientIPErrorCircuitSampleSync(sample("adapter_request_validation", "sig-x"))
	if !decision.Blocked {
		t.Fatalf("total 阈值必须熔断: %+v", decision)
	}
}

func TestClientIPErrorCircuitScopeKeyIsJSONCompatible(t *testing.T) {
	key, ok := clientIPErrorScopeKey(gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", APIKeyID: " key ", ClientIP: "10.0.0.1"})
	if !ok {
		t.Fatal("expected key")
	}
	if key != `{"systemAccountId":"sys","apiKeyId":"key","clientIp":"10.0.0.1"}` {
		t.Fatalf("key=%q（必须与 Node JSON.stringify 逐字节一致）", key)
	}
	if _, ok := clientIPErrorScopeKey(gatewaypreauth.ClientIPErrorCircuitInput{ClientIP: " "}); ok {
		t.Fatal("blank ip must drop the key")
	}
	// APIKeyID 缺省 internal。
	key, _ = clientIPErrorScopeKey(gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"})
	if key != `{"systemAccountId":"sys","apiKeyId":"internal","clientIp":"10.0.0.1"}` {
		t.Fatalf("default apiKeyId key=%q", key)
	}
}

func TestClientIPErrorCircuitMaxSignaturesAndSampleWindows(t *testing.T) {
	circuit, clock := newTestCircuit(t, nil)
	sample := func(signature string) gatewaypreauth.ClientIPErrorCircuitSampleInput {
		return gatewaypreauth.ClientIPErrorCircuitSampleInput{
			SystemAccountID: "sys", ClientIP: "10.0.0.1", Endpoint: "/v1/embeddings",
			Reason: gatewaypreauth.ClientIPErrorCircuitAdapterRequestValidation, Signature: signature,
		}
	}
	// total window 60s + threshold 20：19 个不同 signature 不熔断（与 Node
	// 一致，第 20 发即达 total 阈值）。
	var decision gatewaypreauth.CircuitDecision
	for i := 0; i < clientIPTotalThreshold-1; i += 1 {
		decision = circuit.RecordClientIPErrorCircuitSampleSync(sample("s" + strconv.Itoa(i)))
		if decision.Blocked {
			t.Fatalf("total=%d 不应熔断: %+v", i+1, decision)
		}
	}
	// signature 窗口 30s：推进 31s 后旧 signature 样本全部剪掉；
	// 5 发同一 fresh signature 触发 signature 阈值。
	clock.advance(31 * time.Second)
	decision = gatewaypreauth.CircuitDecision{}
	for i := 0; i < clientIPSignatureThreshold; i += 1 {
		decision = circuit.RecordClientIPErrorCircuitSampleSync(sample("fresh"))
	}
	if !decision.Blocked {
		t.Fatalf("fresh signature 5 连击必须熔断: %+v", decision)
	}
}

func TestUpsertSignatureSamplePrunesOldest(t *testing.T) {
	// Node upsertSignatureSample: signatures 超过 20 时从最旧开始剪。
	entry := clientIPErrorEntry{Samples: []int64{}, Signatures: []signatureSample{}}
	now := int64(1_000_000)
	for i := 0; i < maxSignaturesPerScope+5; i += 1 {
		if got := upsertSignatureSample(&entry, "sig-"+strconv.Itoa(i), now); got != 1 {
			t.Fatalf("fresh signature count=%d want 1", got)
		}
	}
	if len(entry.Signatures) != maxSignaturesPerScope {
		t.Fatalf("signatures=%d want %d", len(entry.Signatures), maxSignaturesPerScope)
	}
	// 最旧的 sig-0..4 被剪掉，保留 sig-5..24。
	if entry.Signatures[0].Signature != "sig-5" || entry.Signatures[len(entry.Signatures)-1].Signature != "sig-24" {
		t.Fatalf("prune order broken: first=%s last=%s", entry.Signatures[0].Signature, entry.Signatures[len(entry.Signatures)-1].Signature)
	}
	// 同 signature 重复命中 → 计数递增。
	if got := upsertSignatureSample(&entry, "sig-24", now+1); got != 2 {
		t.Fatalf("repeat count=%d want 2", got)
	}
	// 窗口外样本（30s）全部剪掉 → signature 移除。
	if got := upsertSignatureSample(&entry, "sig-24", now+31_000); got != 1 {
		t.Fatalf("after window count=%d want 1", got)
	}
}

func TestCircuitDecisionExponentialCapAndRetryAfter(t *testing.T) {
	blockCount := 0
	blockedUntil := (*int64)(nil)
	now := int64(1_000_000)
	// 第 0..4 次：30s, 60s, 120s, 240s, 480s；第 5+ 次：cap 600s。
	// Node: initial * 2^min(count,4)，指数封 4 → 480s 恒定；maxBlockMs=600s
	// 只是上限，实际到不了。
	wants := []int64{30_000, 60_000, 120_000, 240_000, 480_000, 480_000, 480_000}
	for i, want := range wants {
		// 到期后才能再次 open。
		if blockedUntil != nil {
			now = *blockedUntil + 1
		}
		openBlockEntry(&blockCount, &blockedUntil, now, 30_000, 600_000)
		got := *blockedUntil - now
		if got != want {
			t.Fatalf("第 %d 次 block=%d want %d", i, got, want)
		}
	}
	if blockCount != len(wants) {
		t.Fatalf("blockCount=%d", blockCount)
	}
}

func TestCircuitRedisModeMatchesMemoryContract(t *testing.T) {
	server := miniredis.RunT(t)
	redisCircuit, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(redisCircuit.Close)
	input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.1", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}
	ctx := context.Background()
	var decision gatewaypreauth.CircuitDecision
	for i := 0; i < preAuthMissingThreshold; i += 1 {
		decision, err = redisCircuit.RecordPreAuthFailure(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !decision.Blocked {
		t.Fatalf("redis 阈值必须熔断: %+v", decision)
	}
	inspect, err := redisCircuit.InspectPreAuthCircuit(ctx, gatewaypreauth.PreAuthCircuitInput{ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !inspect.Blocked {
		t.Fatal("redis inspect must block")
	}
	// preauth entry key 与 memory 模式一致（redisNamespacedKey 前缀 + entry:）。
	if !server.Exists("juhe-ai:dev:state:gateway-client-ip-error-circuit:entry:preauth:10.0.0.1:missing") {
		t.Fatalf("redis key missing")
	}
	// error circuit sample → success。
	sample := gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys", ClientIP: "10.0.0.1", Endpoint: "/v1/models",
		Reason: gatewaypreauth.ClientIPErrorCircuitInvalidJSON, Signature: "s",
	}
	for i := 0; i < clientIPSignatureThreshold; i += 1 {
		decision, err = redisCircuit.RecordClientIPErrorCircuitSample(ctx, sample)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !decision.Blocked {
		t.Fatalf("redis signature 阈值必须熔断: %+v", decision)
	}
	if err := redisCircuit.RecordClientIPErrorCircuitSuccess(ctx, gatewaypreauth.ClientIPErrorCircuitInput{
		SystemAccountID: "sys", ClientIP: "10.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	inspect2, err := redisCircuit.InspectClientIPErrorCircuit(ctx, gatewaypreauth.ClientIPErrorCircuitInput{SystemAccountID: "sys", ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if inspect2.Blocked {
		t.Fatalf("success 后必须恢复: %+v", inspect2)
	}
	// Redis 不可用时报错透传（不静默降级）。
	server.Close()
	if _, err := redisCircuit.RecordPreAuthFailure(ctx, input); err == nil {
		t.Fatal("Redis 不可用时必须报错")
	}
}

func TestNormalizeSignaturePart(t *testing.T) {
	if got := normalizeSignaturePart("  Ada   Delta\n\tJSON  "); got != "ada delta json" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("x", 300)
	if len(normalizeSignaturePart(long)) != 240 {
		t.Fatal("240 char cap")
	}
}
