package gatewaydispatch

// 账户内 API Key 会话亲和：最终成功写回闭包的构造臂（设计 §5/§9——2xx 头
// 阶段只构造不执行；链面 confirmProtocolSuccessSideEffects 消费一次）。

import (
	"context"
	"testing"
)

// countingKeyFingerprintPort 记录 RememberApiKeyFingerprintAsync 调用参数；
// 其余端口方法由内嵌 nil 接口兜底（未消费不 panic）。
type countingKeyFingerprintPort struct {
	SessionAffinityPort
	calls []string
}

func (p *countingKeyFingerprintPort) RememberApiKeyFingerprintAsync(_ context.Context, sessionAffinityKey, accountID, fingerprint string) {
	p.calls = append(p.calls, sessionAffinityKey+"|"+accountID+"|"+fingerprint)
}

func TestNewConfirmSessionApiKeyFingerprintFuncArms(t *testing.T) {
	ctx := context.Background()
	fingerprint := apiKeyFingerprint(testRotationSecret, "k0")
	accountWithFingerprint := AccountCandidate{ID: "acc-1", SelectedAPIKeyFingerprint: &fingerprint}

	t.Run("nil port yields nil", func(t *testing.T) {
		if got := newConfirmSessionApiKeyFingerprintFunc(nil, ctx, "sess", accountWithFingerprint); got != nil {
			t.Fatal("nil 端口应返回 nil 闭包")
		}
	})

	t.Run("empty session key yields nil", func(t *testing.T) {
		port := &countingKeyFingerprintPort{}
		if got := newConfirmSessionApiKeyFingerprintFunc(port, ctx, "", accountWithFingerprint); got != nil {
			t.Fatal("空会话键应返回 nil 闭包")
		}
	})

	t.Run("nil selected fingerprint yields nil", func(t *testing.T) {
		port := &countingKeyFingerprintPort{}
		account := AccountCandidate{ID: "acc-1"}
		if got := newConfirmSessionApiKeyFingerprintFunc(port, ctx, "sess", account); got != nil {
			t.Fatal("无选中指纹（非池隔离路径）应返回 nil 闭包")
		}
	})

	t.Run("invoke forwards key account fingerprint", func(t *testing.T) {
		port := &countingKeyFingerprintPort{}
		closure := newConfirmSessionApiKeyFingerprintFunc(port, ctx, "sess", accountWithFingerprint)
		if closure == nil {
			t.Fatal("完整输入应返回可执行闭包")
		}
		closure()
		closure()
		if len(port.calls) != 2 ||
			port.calls[0] != "sess|acc-1|"+fingerprint {
			t.Fatalf("闭包应携带会话键/账户/指纹调用端口，实际 %v", port.calls)
		}
	})
}
