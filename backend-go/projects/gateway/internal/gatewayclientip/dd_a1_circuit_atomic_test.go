package gatewayclientip

// DD-A1 钉住测试：preauth / client-IP error 两条 Redis 熔断计数路径必须原子
//（store.UpdateJSON，WATCH 乐观重试）。并发 N 次打点后最终样本数必须等于 N
//（非原子的普通 GET/SET 会因丢更新少于 N，熔断灵敏度退化）。
import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// readRedisCircuitEntry 直接从 miniredis 读回 entry JSON（断言面：最终样本数）。
func readRedisCircuitEntry(t *testing.T, server *miniredis.Miniredis, redisKey string) (samples int, found bool) {
	t.Helper()
	raw, err := server.Get(redisKey)
	if err != nil {
		t.Fatalf("miniredis get: %v", err)
	}
	if raw == "" {
		return 0, false
	}
	var decoded struct {
		Samples []int64 `json:"samples"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("entry 解码: %v (raw=%q)", err, raw)
	}
	return len(decoded.Samples), true
}

// TestDDA1RecordPreAuthFailureConcurrentNoLostUpdates：并发 30 次同 IP 记录
//（阈值 40 之下，不截断样本）→ 最终样本数必须为 30。
func TestDDA1RecordPreAuthFailureConcurrentNoLostUpdates(t *testing.T) {
	server := miniredis.RunT(t)
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)

	const workers = 30
	ctx := context.Background()
	input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.7", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := circuit.RecordPreAuthFailure(ctx, input); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发记录失败: %v", err)
	}
	samples, found := readRedisCircuitEntry(t, server, "juhe-ai:dev:state:gateway-client-ip-error-circuit:entry:preauth:10.0.0.7:missing")
	if !found {
		t.Fatal("entry 必须存在")
	}
	if samples != workers {
		t.Fatalf("最终样本数 = %d, want %d（并发丢更新）", samples, workers)
	}
}

// TestDDA1RecordClientIPErrorSampleConcurrentNoLostUpdates：错误熔断臂并发
// 10 次采样（每次不同 signature 避开签名阈值 5；总阈值 20 之下不熔断）→
// 最终总样本数为 10。
func TestDDA1RecordClientIPErrorSampleConcurrentNoLostUpdates(t *testing.T) {
	server := miniredis.RunT(t)
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)

	const workers = 10
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		input := gatewaypreauth.ClientIPErrorCircuitSampleInput{
			SystemAccountID: "sys_dd_a1",
			APIKeyID:        "key_dd_a1",
			ClientIP:        "10.0.0.9",
			Reason:          circuitReasonAdapterRequestValidation,
			Signature:       "sig-" + strconv.Itoa(i),
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := circuit.RecordClientIPErrorCircuitSample(ctx, input); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发采样失败: %v", err)
	}
	// scope key 与 memory 臂一致：JSON.stringify({systemAccountId, apiKeyId, clientIp})。
	redisKey := "juhe-ai:dev:state:gateway-client-ip-error-circuit:entry:" +
		`{"systemAccountId":"sys_dd_a1","apiKeyId":"key_dd_a1","clientIp":"10.0.0.9"}`
	samples, found := readRedisCircuitEntry(t, server, redisKey)
	if !found {
		t.Fatal("entry 必须存在")
	}
	if samples != workers {
		t.Fatalf("最终总样本数 = %d, want %d（并发丢更新）", samples, workers)
	}
}

// TestDDA1PreAuthThresholdBlockSemantics：Lua 记录路径的阈值/熔断语义与
// memory 臂一致——顺序打点达阈值（40）后熔断，其后继续打点 blocked 早退
// 不再追加样本（样本冻结在阈值截断值），决策携带 Retry-After。
func TestDDA1PreAuthThresholdBlockSemantics(t *testing.T) {
	server := miniredis.RunT(t)
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)
	ctx := context.Background()
	input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.11", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}

	var decision gatewaypreauth.CircuitDecision
	for i := 0; i < preAuthMissingThreshold; i++ {
		decision, err = circuit.RecordPreAuthFailure(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !decision.Blocked || decision.Reason != string(gatewaypreauth.PreAuthFailureMissingBearerToken) {
		t.Fatalf("达阈值必须熔断: %+v", decision)
	}
	if decision.RetryAfterSeconds == nil || *decision.RetryAfterSeconds < 1 {
		t.Fatalf("熔断决策必须携带 Retry-After: %+v", decision)
	}
	// blocked 后继续打点：样本冻结（阈值截断），不再追加。
	for i := 0; i < 5; i++ {
		if _, err := circuit.RecordPreAuthFailure(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	samples, _ := readRedisCircuitEntry(t, server, "juhe-ai:dev:state:gateway-client-ip-error-circuit:entry:preauth:10.0.0.11:missing")
	if samples != preAuthMissingThreshold {
		t.Fatalf("blocked 后样本数 = %d, want %d（早退不得追加）", samples, preAuthMissingThreshold)
	}
}

// TestDDA1ClientIPErrorSignatureBlockSemantics：错误熔断臂同一签名并发采样
// 达签名阈值（5）→ 熔断；signature 元组保持 Node 形态（["sig",[ms...]]）。
func TestDDA1ClientIPErrorSignatureBlockSemantics(t *testing.T) {
	server := miniredis.RunT(t)
	circuit, err := NewErrorCircuit(ErrorCircuitOptions{
		RuntimeStateDriver: RuntimeStateDriverRedis,
		StateRedisURL:      "redis://" + server.Addr(),
		RedisNamespace:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(circuit.Close)
	ctx := context.Background()
	input := gatewaypreauth.ClientIPErrorCircuitSampleInput{
		SystemAccountID: "sys_dd_sig",
		APIKeyID:        "key_dd_sig",
		ClientIP:        "10.0.0.12",
		Reason:          circuitReasonAdapterRequestValidation,
	}
	var decision gatewaypreauth.CircuitDecision
	for i := 0; i < clientIPSignatureThreshold; i++ {
		decision, err = circuit.RecordClientIPErrorCircuitSample(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !decision.Blocked {
		t.Fatalf("签名达阈值必须熔断: %+v", decision)
	}
	raw, err := server.Get("juhe-ai:dev:state:gateway-client-ip-error-circuit:entry:" +
		`{"systemAccountId":"sys_dd_sig","apiKeyId":"key_dd_sig","clientIp":"10.0.0.12"}`)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Signatures [][]any `json:"signatures"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("signature 元组解码: %v (raw=%q)", err, raw)
	}
	if len(decoded.Signatures) != 1 {
		t.Fatalf("signature 条目数 = %d, want 1 (raw=%q)", len(decoded.Signatures), raw)
	}
	if _, ok := decoded.Signatures[0][1].([]any); !ok {
		t.Fatalf("signature 元组形态必须为 [\"sig\",[ms...]]: %v", decoded.Signatures[0])
	}
}

// TestDDRcB1BlockedEarlyExitFailureCountMirrorsMemory（DD-RC-B1）：entry 已
// blocked 时再次记录走 blocked 早退（不写回），返回 decision 的 failureCount
// 必须等于 entry 存量样本数——与 memory 臂 blockedCircuitDecision 的
// len(samples) 同值——不得硬编码 0。preauth 臂与 client-IP error 臂各验一遍，
// Redis Lua 臂与 memory 臂对照同值。
func TestDDRcB1BlockedEarlyExitFailureCountMirrorsMemory(t *testing.T) {
	t.Run("preauth", func(t *testing.T) {
		input := gatewaypreauth.PreAuthFailureInput{ClientIP: "10.0.0.21", Reason: gatewaypreauth.PreAuthFailureMissingBearerToken}

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
		ctx := context.Background()
		var decision gatewaypreauth.CircuitDecision
		for i := 0; i < preAuthMissingThreshold; i++ {
			decision, err = redisCircuit.RecordPreAuthFailure(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
		}
		// 已 blocked 后再记录一次：blocked 早退路径。
		if decision, err = redisCircuit.RecordPreAuthFailure(ctx, input); err != nil {
			t.Fatal(err)
		}
		if !decision.Blocked {
			t.Fatalf("blocked entry 的再次记录必须早退为 blocked: %+v", decision)
		}
		if decision.FailureCount == nil || *decision.FailureCount != int64(preAuthMissingThreshold) {
			t.Fatalf("blocked 早退 failureCount = %+v, want %d（存量样本数）", decision.FailureCount, preAuthMissingThreshold)
		}

		// memory 臂对照：同序列后同位置 failureCount 同值。
		memoryCircuit, err := NewErrorCircuit(ErrorCircuitOptions{RuntimeStateDriver: RuntimeStateDriverMemory})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(memoryCircuit.Close)
		for i := 0; i < preAuthMissingThreshold; i++ {
			memoryCircuit.RecordGatewayPreAuthFailure(input)
		}
		memoryDecision := memoryCircuit.RecordGatewayPreAuthFailure(input)
		if !memoryDecision.Blocked || memoryDecision.FailureCount == nil || *memoryDecision.FailureCount != *decision.FailureCount {
			t.Fatalf("memory 臂对照不一致: redis=%+v memory=%+v", decision, memoryDecision)
		}
	})

	t.Run("client_ip_error", func(t *testing.T) {
		input := gatewaypreauth.ClientIPErrorCircuitSampleInput{
			SystemAccountID: "sys_dd_rc_b1",
			APIKeyID:        "key_dd_rc_b1",
			ClientIP:        "10.0.0.22",
			Reason:          circuitReasonAdapterRequestValidation,
		}

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
		ctx := context.Background()
		var decision gatewaypreauth.CircuitDecision
		for i := 0; i < clientIPSignatureThreshold; i++ {
			decision, err = redisCircuit.RecordClientIPErrorCircuitSample(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
		}
		// 已 blocked 后再采样一次：blocked 早退路径。
		if decision, err = redisCircuit.RecordClientIPErrorCircuitSample(ctx, input); err != nil {
			t.Fatal(err)
		}
		if !decision.Blocked {
			t.Fatalf("blocked entry 的再次采样必须早退为 blocked: %+v", decision)
		}
		if decision.FailureCount == nil || *decision.FailureCount != int64(clientIPSignatureThreshold) {
			t.Fatalf("blocked 早退 failureCount = %+v, want %d（存量样本数）", decision.FailureCount, clientIPSignatureThreshold)
		}

		// memory 臂对照：同序列后同位置 failureCount 同值。
		memoryCircuit, err := NewErrorCircuit(ErrorCircuitOptions{RuntimeStateDriver: RuntimeStateDriverMemory})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(memoryCircuit.Close)
		for i := 0; i < clientIPSignatureThreshold; i++ {
			memoryCircuit.RecordClientIPErrorCircuitSampleSync(input)
		}
		memoryDecision := memoryCircuit.RecordClientIPErrorCircuitSampleSync(input)
		if !memoryDecision.Blocked || memoryDecision.FailureCount == nil || *memoryDecision.FailureCount != *decision.FailureCount {
			t.Fatalf("memory 臂对照不一致: redis=%+v memory=%+v", decision, memoryDecision)
		}
	})
}
