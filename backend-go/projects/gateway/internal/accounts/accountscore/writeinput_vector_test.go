package accountscore

import "testing"

// 2026-11-20 向量/重排协议补齐批：只声明 embeddings_json/rerank_json 的专用
// 账户没有 chat/media 检查形态，健康检查三件套白名单必须接受这两个值，否则
// 创建链在校验阶段直接 400。两者仅作持久化诊断形态，不派发自动探针。
func TestAccountHealthCheckEndpointModesIncludeVectorModes(t *testing.T) {
	if len(AccountHealthCheckEndpointModes) != 27 {
		t.Fatalf("健康检查写侧白名单 = %d 值，want 27", len(AccountHealthCheckEndpointModes))
	}
	for _, mode := range []string{"embeddings_json", "rerank_json"} {
		if !AccountHealthCheckEndpointModes[mode] {
			t.Fatalf("健康检查写侧白名单缺少 %s（专用向量/重排账户无法创建）", mode)
		}
	}
}
