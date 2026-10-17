package gatewayresponse

import (
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 三层删除端口（策略路由分组会话亲和设计 §5.2 删除矩阵）：装配了
// SessionAffinityLayersPort 的效果面在「既有响应失败 forget」触发类上按
// SessionAffinityLayers 一次性删除组层（可选）与账户/Key 层；未装配扩展面
// 的效果面回落单层 ForgetSessionAffinity，组键不丢失语义（实现侧跳过空键）。

type recordingLayerEffects struct {
	mockAccountEffects
	mu     sync.Mutex
	layers []SessionAffinityLayers
}

func (e *recordingLayerEffects) ForgetSessionAffinityLayers(layers SessionAffinityLayers) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.layers = append(e.layers, layers)
}

func TestForgetSessionAffinityForFailureUsesLayersWhenAvailable(t *testing.T) {
	effects := &recordingLayerEffects{}
	input := &HandleUpstreamResponseInput{
		Account:            OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1"}},
		SessionAffinityKey: "sess-key",
		GroupAffinityKey:   "grp-key",
		Deps:               &FinalizationDeps{AccountEffects: effects},
	}
	input.forgetSessionAffinityForFailure()
	if len(effects.layers) != 1 {
		t.Fatalf("layers 调用次数 = %d, want 1", len(effects.layers))
	}
	layers := effects.layers[0]
	if layers.GroupAffinityKey != "grp-key" || layers.SessionAffinityKey != "sess-key" || layers.AccountID != "acc-1" {
		t.Fatalf("layers = %+v", layers)
	}
	if len(effects.affinity) != 0 {
		t.Fatalf("扩展面装配时不得再走单层路径：%v", effects.affinity)
	}
}

func TestForgetSessionAffinityForFailureFallsBackToSingleLayer(t *testing.T) {
	effects := &mockAccountEffects{}
	input := &HandleUpstreamResponseInput{
		Account:            OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1"}},
		SessionAffinityKey: "sess-key",
		GroupAffinityKey:   "grp-key",
		Deps:               &FinalizationDeps{AccountEffects: effects},
	}
	input.forgetSessionAffinityForFailure()
	if len(effects.affinity) != 1 || effects.affinity[0] != "sess-key:acc-1" {
		t.Fatalf("单层回落 = %v, want [sess-key:acc-1]", effects.affinity)
	}
}

func TestForgetSessionAffinityForFailureMissingGroupKeyKeepsLayersShape(t *testing.T) {
	effects := &recordingLayerEffects{}
	input := &HandleUpstreamResponseInput{
		Account:            OpenAIAccountView{Account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1"}},
		SessionAffinityKey: "sess-key",
		Deps:               &FinalizationDeps{AccountEffects: effects},
	}
	input.forgetSessionAffinityForFailure()
	if len(effects.layers) != 1 {
		t.Fatalf("layers 调用次数 = %d, want 1", len(effects.layers))
	}
	// 组键缺失 = 组层缺失（未完成身份解析 / 无会话键 / 模式范围外）：实现只
	// 删账户/Key 层，键位保持空串，不猜测或派生空键（§3.3）。
	if effects.layers[0].GroupAffinityKey != "" {
		t.Fatalf("组键位 = %q, want 空串", effects.layers[0].GroupAffinityKey)
	}
}
