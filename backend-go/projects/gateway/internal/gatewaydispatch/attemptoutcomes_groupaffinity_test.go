package gatewaydispatch

import (
	"context"
	"sync"
	"testing"
)

// 组级会话黏性写回闭包（策略路由分组会话亲和设计 §5）：nil 端口 / 空键 / 空
// 落组均返回 nil 句柄（链面结算自然跳过）；命中时捕获 WithoutCancel 上下文
// 并携带最终落组。

type recordingGroupAffinityPort struct {
	mu    sync.Mutex
	calls []string
}

func (p *recordingGroupAffinityPort) RememberGroupAsync(_ context.Context, groupAffinityKey, routeStrategyID, groupID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, groupAffinityKey+"|"+routeStrategyID+"|"+groupID)
}

func TestNewConfirmGroupAffinityFuncGuards(t *testing.T) {
	port := &recordingGroupAffinityPort{}
	cases := []struct {
		name    string
		port    GroupAffinityPort
		key     string
		groupID string
	}{
		{"nil port", nil, "k", "g"},
		{"empty key", port, "", "g"},
		{"empty group", port, "k", ""},
	}
	for _, tc := range cases {
		if got := newConfirmGroupAffinityFunc(tc.port, context.Background(), tc.key, "strategy-1", tc.groupID); got != nil {
			t.Fatalf("%s: 应返回 nil 句柄", tc.name)
		}
	}
}

func TestNewConfirmGroupAffinityFuncCarriesFinalGroup(t *testing.T) {
	port := &recordingGroupAffinityPort{}
	confirm := newConfirmGroupAffinityFunc(port, context.Background(), "aff-key", "strategy-1", "grp-final")
	if confirm == nil {
		t.Fatal("应返回写回句柄")
	}
	// 消费一次：写回键 + 策略 + 最终落组（组 → 账户 → Key 顺序由链面保证）。
	confirm()
	confirm()
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.calls) != 2 {
		t.Fatalf("写回次数 = %d, want 2（句柄可重复触发，由链面 once 消费语义保证）", len(port.calls))
	}
	if port.calls[0] != "aff-key|strategy-1|grp-final" {
		t.Fatalf("写回内容 = %q, want aff-key|strategy-1|grp-final", port.calls[0])
	}
}
