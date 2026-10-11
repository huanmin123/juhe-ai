package upstreamenvelope

// AuthNone 显式无认证策略契约（W2 F3 接线裁决 2026-10-12）：仅供已解析为
// 无凭据请求的调用方显式选择，Apply 不写任何头；不是缺省/兜底——零值策略
// 与其余策略的空 token 仍显式失败，不静默发出无认证请求。

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuthNoneAppliesNoHeader(t *testing.T) {
	header := http.Header{}
	if err := (Auth{Strategy: AuthNone}).Apply(header); err != nil {
		t.Fatalf("AuthNone Apply err=%v", err)
	}
	if len(header) != 0 {
		t.Fatalf("AuthNone 不得写任何头: %v", header)
	}
	// 附加头映射同样不应用：AuthNone 的契约是本策略整体沉默，不与
	// ExtraHeaders 组合出半认证形态。
	header = http.Header{}
	if err := (Auth{Strategy: AuthNone, ExtraHeaders: map[string]string{"X-Trace": "w2"}}).Apply(header); err != nil {
		t.Fatalf("AuthNone Apply err=%v", err)
	}
	if got := header.Get("X-Trace"); got != "" {
		t.Fatalf("AuthNone 不得应用 ExtraHeaders: X-Trace=%q", got)
	}
}

func TestAuthNoneIsNotADefaultOrFallback(t *testing.T) {
	// 零值策略（未选择）保持现状：显式失败，不回落 AuthNone。
	header := http.Header{}
	if err := (Auth{}).Apply(header); err == nil || !strings.Contains(err.Error(), "认证凭据为空") {
		t.Fatalf("零值策略必须显式失败, err=%v", err)
	}
	if len(header) != 0 {
		t.Fatalf("失败路径不得写头: %v", header)
	}
	// 其余全部带凭据策略：空 token 仍无条件 fail-closed。
	for _, strategy := range []AuthStrategy{AuthBearer, AuthOAuth, AuthAnthropicAPIKey, AuthGeminiAPIKey} {
		header := http.Header{}
		err := (Auth{Strategy: strategy}).Apply(header)
		if err == nil || !strings.Contains(err.Error(), "认证凭据为空") {
			t.Fatalf("策略 %s 空 token 必须 fail-closed, err=%v", strategy, err)
		}
		if len(header) != 0 {
			t.Fatalf("策略 %s 空 token 失败路径不得写头: %v", strategy, header)
		}
	}
}
