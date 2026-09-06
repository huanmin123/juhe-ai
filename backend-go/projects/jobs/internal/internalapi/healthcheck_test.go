package internalapi

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

// 测试签名密钥：32 字节 canonical base64url。
func testSigningKey() string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type fakeBoundary struct {
	account      HealthCheckAccountRef
	inputVersion int64
	revisions    HealthCheckRevisions
	ok           bool
	err          error
}

func (f *fakeBoundary) CurrentProbeInput(context.Context, string) (HealthCheckAccountRef, int64, HealthCheckRevisions, bool, error) {
	return f.account, f.inputVersion, f.revisions, f.ok, f.err
}

func testHealthOptions(t *testing.T, boundary HealthCheckBoundary) (HealthCheckDispatchOptions, string) {
	t.Helper()
	root := t.TempDir()
	options := HealthCheckDispatchOptions{
		InputRoot:       root,
		SigningKey:      testSigningKey(),
		ProbeDeadlineMS: 60_000,
		NowMS:           func() int64 { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() },
		Boundary:        boundary,
	}
	return options, root
}

// 派发 outcome 矩阵：dispatch_rejected / input_unavailable / queued。
func TestDispatchAccountHealthCheckOutcomeMatrix(t *testing.T) {
	account := HealthCheckAccountRef{ID: "acc-1", ConfigRevision: 4, DispatchRevision: 9}
	t.Run("空账户 ID → dispatch_rejected", func(t *testing.T) {
		options, _ := testHealthOptions(t, &fakeBoundary{account: account, inputVersion: 2, revisions: HealthCheckRevisions{ConfigRevision: 4, DispatchRevision: 9}, ok: true})
		outcome, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "  ", "manual_retry", "", nil, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		if outcome != rejectedDispatchOutcome("dispatch_rejected") {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("缺 input root → input_unavailable", func(t *testing.T) {
		options, _ := testHealthOptions(t, &fakeBoundary{account: account, inputVersion: 2, ok: true})
		options.InputRoot = "  "
		outcome, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "manual_retry", "", nil, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.DecisionCode != "input_unavailable" || outcome.Outcome != "rejected" {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("缺签名密钥 → input_unavailable", func(t *testing.T) {
		options, _ := testHealthOptions(t, &fakeBoundary{account: account, inputVersion: 2, ok: true})
		options.SigningKey = ""
		outcome, _ := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "manual_retry", "", nil, nil, options)
		if outcome.DecisionCode != "input_unavailable" {
			t.Fatalf("outcome = %+v", outcome)
		}
	})
	t.Run("账户不在 J1 冻结范围 → queued + source fence unknown", func(t *testing.T) {
		options, root := testHealthOptions(t, &fakeBoundary{account: account, ok: false})
		settled := ""
		options.SettleSourceFence = func(_ context.Context, _ HealthCheckSourceFence, state string) error {
			settled = state
			return nil
		}
		fence := &HealthCheckSourceFence{StateKey: "sk", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "sf", RuntimeKey: "acc-1", ProbeGeneration: 2, ConfigRevision: 4}
		outcome, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "", fence, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != "queued" || outcome.DecisionCode != "queued" || outcome.TargetRole != "go-jobs" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if settled != "unknown" {
			t.Fatalf("source fence 应结算 unknown，got %q", settled)
		}
		entries, _ := os.ReadDir(root)
		if len(entries) != 0 {
			t.Fatal("范围外账户不得发布 request 文件")
		}
	})
	// 审查轮换七 #5：revisions/account 的 config_revision 在唯一生产 Boundary
	// （worker_health_dispatch.go）读自同一列，自比较恒假，该分支及对应的
	// 「revision 不一致」子用例已删除。
	t.Run("boundary 错误上抛", func(t *testing.T) {
		options, _ := testHealthOptions(t, &fakeBoundary{err: errors.New("db down")})
		if _, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "manual_retry", "", nil, nil, options); err == nil {
			t.Fatal("应上抛 boundary 错误")
		}
	})
}

// 正常派发：request 文件按 Node 协议签名落盘，可被 accounthealth 消费侧验证。
func TestDispatchAccountHealthCheckPublishesSignedRequest(t *testing.T) {
	account := HealthCheckAccountRef{ID: "acc-1", ConfigRevision: 4, DispatchRevision: 9}
	options, root := testHealthOptions(t, &fakeBoundary{
		account:      account,
		inputVersion: 2,
		revisions:    HealthCheckRevisions{ConfigRevision: 4, DispatchRevision: 9},
		ok:           true,
	})
	outcome, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "trace-1", nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != "queued" || !strings.HasPrefix(outcome.RequestID, HealthCheckProbeRequestIDPrefix+"") {
		t.Fatalf("outcome = %+v", outcome)
	}
	// 文件名 = sha256(requestId) + 后缀。
	target, err := AccountHealthJobsRequestPath(root, outcome.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("request 文件应已发布: %v", err)
	}
	// 用 accounthealth 消费侧同一验证链路回放。
	requests, err := accounthealth.LoadSignedProbeRequests(root, map[string][]byte{
		"runtime-v1": mustDecodeBase64URL(options.SigningKey),
	})
	if err != nil {
		t.Fatalf("消费侧验证失败: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("request 数 = %d", len(requests))
	}
	request := requests[0]
	if request.RequestID != outcome.RequestID || request.AccountID != "acc-1" || request.Reason != "gateway_failure" {
		t.Fatalf("request 内容不符: %+v", request)
	}
	if request.InputVersion != 2 || request.ConfigRevision != 4 || request.DispatchRevision != 9 {
		t.Fatalf("revision 字段不符: %+v", request)
	}
	if request.MutateAccount != true {
		t.Fatal("无 source fence 时 mutate_account 必须为 true")
	}
	if !request.Deadline.After(time.UnixMilli(options.NowMS())) {
		t.Fatal("deadline 必须在未来")
	}
	_ = raw
}

// fence 校验矩阵对齐 Node publishAccountHealthJobsProbeRequest。
func TestDispatchAccountHealthCheckFenceValidation(t *testing.T) {
	account := HealthCheckAccountRef{ID: "acc-1", ConfigRevision: 4, DispatchRevision: 9}
	baseBoundary := &fakeBoundary{account: account, inputVersion: 2, revisions: HealthCheckRevisions{ConfigRevision: 4, DispatchRevision: 9}, ok: true}

	t.Run("source fence revision 不一致报错", func(t *testing.T) {
		options, _ := testHealthOptions(t, baseBoundary)
		fence := &HealthCheckSourceFence{StateKey: "sk", AccountID: "acc-1", SourceGeneration: 1, SourceFenceID: "sf", RuntimeKey: "acc-1", ProbeGeneration: 2, ConfigRevision: 5}
		if _, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "", fence, nil, options); err == nil {
			t.Fatal("应报 revision 不一致")
		}
	})
	t.Run("key-model fence 无效 hash 报错", func(t *testing.T) {
		options, _ := testHealthOptions(t, baseBoundary)
		fence := &HealthCheckKeyModelFence{CapabilityHash: "nothex", KeyFingerprint: "fp", DispatchRevision: 9, OwnerID: "owner"}
		if _, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "", nil, fence, options); err == nil {
			t.Fatal("应报 fence 无效")
		}
	})
	t.Run("key-model fence 合定时 mutate 仍为 true 且文件落盘", func(t *testing.T) {
		options, root := testHealthOptions(t, baseBoundary)
		fence := &HealthCheckKeyModelFence{CapabilityHash: strings.Repeat("a", 64), KeyFingerprint: "fp", DispatchRevision: 9, OwnerID: "owner"}
		outcome, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "", nil, fence, options)
		if err != nil {
			t.Fatal(err)
		}
		requests, err := accounthealth.LoadSignedProbeRequests(root, map[string][]byte{
			"runtime-v1": mustDecodeBase64URL(options.SigningKey),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(requests) != 1 || !requests[0].MutateAccount {
			t.Fatalf("requests=%+v", requests)
		}
		if outcome.RequestID == "" {
			t.Fatal("应有 request ID")
		}
	})
	t.Run("deadline 过去报错", func(t *testing.T) {
		options, _ := testHealthOptions(t, baseBoundary)
		options.ProbeDeadlineMS = 0
		if _, err := DispatchAccountHealthCheckWithOutcome(context.Background(), "acc-1", "gateway_failure", "", nil, nil, options); err == nil {
			t.Fatal("deadline 必须在未来")
		}
	})
}

// 布尔便捷包装与 rejected 语义。
func TestDispatchAccountHealthCheckBooleanWrapper(t *testing.T) {
	account := HealthCheckAccountRef{ID: "acc-1", ConfigRevision: 4, DispatchRevision: 9}
	options, _ := testHealthOptions(t, &fakeBoundary{account: account, inputVersion: 2, revisions: HealthCheckRevisions{ConfigRevision: 4, DispatchRevision: 9}, ok: true})
	accepted, err := DispatchAccountHealthCheck(context.Background(), "acc-1", "manual_retry", "", options)
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	rejected, err := DispatchAccountHealthCheck(context.Background(), "", "manual_retry", "", options)
	if err != nil || rejected {
		t.Fatalf("rejected=%v err=%v", rejected, err)
	}
}

func mustDecodeBase64URL(value string) []byte {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

// 签名格式与 Node 消费侧互认：SignAccountHealthPayload 产生的 envelope 字段
// 与 Node account-health-jobs-input.protocol.ts 一致。
func TestSignAccountHealthPayloadEnvelope(t *testing.T) {
	signingKey := testSigningKey()
	payload := map[string]any{"request_id": "j1-x"}
	raw, err := SignAccountHealthPayload(payload, signingKey, "runtime-v1")
	if err != nil {
		t.Fatal(err)
	}
	// 直接走 accounthealth 验证。
	verified, err := accounthealth.VerifySignedPayload(raw, map[string][]byte{"runtime-v1": mustDecodeBase64URL(signingKey)})
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if !strings.Contains(string(verified), "j1-x") {
		t.Fatalf("payload 不符: %s", verified)
	}
	// 错误密钥必须失败。
	if _, err := accounthealth.VerifySignedPayload(raw, map[string][]byte{"runtime-v1": []byte("wrong-key-bytes-wrong-key-bytes!")}); err == nil {
		t.Fatal("错误密钥应验证失败")
	}
	// 非 canonical 密钥必须拒绝。
	if _, err := SignAccountHealthPayload(payload, base64.StdEncoding.EncodeToString(make([]byte, 32)), "runtime-v1"); err == nil {
		t.Fatal("非 canonical base64url 密钥应拒绝")
	}
}
