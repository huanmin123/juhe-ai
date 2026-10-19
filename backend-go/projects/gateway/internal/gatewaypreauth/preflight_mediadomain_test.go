package gatewaypreauth

// T10 对话与非对话媒体调度分离落地：preflight 的调度域不变量回归
// （对话与非对话媒体调度分离设计 §5）。媒体创建域（StatelessMediaDispatch）
// 的 preflight 结果必须满足三类亲和输入恒空：
//
//	SessionAffinityKey = ""
//	GroupAffinityKey = ""
//	InteractionResourceAffinity = nil
//
// 全部纯内存 fake（groupsticky 夹具族），Mock 优先、结果稳定可回放。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func mediaDomainPreflightService(t *testing.T, affinity *fakeGroupAffinityPort, sessionAffinity *fakeSessionAffinity) (*Service, *groupStickyRuntimeCache) {
	t.Helper()
	staticRow := groupStickyStaticRow()
	// image lane 的协调预算按图像墙钟档位派生，夹具补齐图像超时设置。
	staticRow.Settings.ImageRequestWallTimeoutSeconds = 300
	cache := &groupStickyRuntimeCache{
		fakeRuntimeCache: fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": staticRow},
			groupAccess:  &gatewayruntimecache.GroupUsageAccessMetadata{},
			accounts:     staticRow.Accounts,
		},
		staticRow: staticRow,
	}
	account := staticRow.Accounts[0]
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = cache
		s.GroupAffinity = affinity
		s.SessionAffinity = sessionAffinity
		s.SessionIdentity = &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_1", ConversationKey: "conv_generic"}}
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})
	return service, cache
}

// TestMediaDispatchPreflightClearsAffinityInputs：媒体创建 lane（image/audio/
// video）携带会话身份时，preflight 不查询组亲和黏住、不派生组亲和键、不解析
// 会话亲和键；模式状态推进保持每请求一次（权重/轮询语义不变，§3.1 关键点 3）；
// 既有组绑定不被媒体请求消费（grp_a 不落 grp_b）。
func TestMediaDispatchPreflightClearsAffinityInputs(t *testing.T) {
	for _, lane := range []string{"image", "audio", "video"} {
		t.Run(lane, func(t *testing.T) {
			affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
			service, cache := mediaDomainPreflightService(t, affinity, &fakeSessionAffinity{fromIdentity: "sak:conv_generic"})

			context, recorder := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true, RequestLane: gatewayproto.RequestLane(lane)})
			if recorder.Code >= 400 {
				t.Fatalf("成功路径不得写失败响应: %d", recorder.Code)
			}
			// 三类亲和输入恒空（设计 §5 preflight 不变量）。
			if context.SessionAffinityKey != "" {
				t.Fatalf("lane %q SessionAffinityKey = %q, want 空串", lane, context.SessionAffinityKey)
			}
			if context.GroupAffinityKey != "" {
				t.Fatalf("lane %q GroupAffinityKey = %q, want 空串", lane, context.GroupAffinityKey)
			}
			if context.InteractionResourceAffinity != nil {
				t.Fatalf("lane %q InteractionResourceAffinity 必须为 nil", lane)
			}
			// 组亲和零查询：derive / sticky 都不触达。
			if derives, stickies := affinity.counts(); derives != 0 || stickies != 0 {
				t.Fatalf("lane %q 组亲和查询 derive=%d sticky=%d, want 0/0", lane, derives, stickies)
			}
			// 模式状态推进保持每请求一次（权重/轮询仍参与组选择）。
			selects := cache.recordedSelects()
			if len(selects) != 1 {
				t.Fatalf("lane %q select 调用 = %d, want 1", lane, len(selects))
			}
			if selects[0].PreferredGroupID != "" || !selects[0].AdvanceOnStickyMiss {
				t.Fatalf("lane %q select 入参 = %+v, want 空黏住 + 推进一次", lane, selects[0])
			}
			// 既有组绑定不被媒体请求消费：按存储序落首组。
			if context.UsageContext.GroupID != "grp_a" {
				t.Fatalf("lane %q 落组 = %q, want grp_a（媒体请求不消费会话黏住）", lane, context.UsageContext.GroupID)
			}
		})
	}
}

// TestConversationDispatchPreflightKeepsAffinityInputs：对照组——同一夹具下
// text lane 的会话亲和与组亲和照常解析（对话域行为不变）。
func TestConversationDispatchPreflightKeepsAffinityInputs(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_generic": "grp_b"}}
	service, _ := mediaDomainPreflightService(t, affinity, &fakeSessionAffinity{fromIdentity: "sak:conv_generic"})

	context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true, RequestLane: gatewayproto.RequestLane("text")})
	if context.SessionAffinityKey != "sak:conv_generic" {
		t.Fatalf("text 对照组 SessionAffinityKey = %q, want sak:conv_generic（对话域行为不变）", context.SessionAffinityKey)
	}
	if context.GroupAffinityKey != "gak:conv_generic" {
		t.Fatalf("text 对照组 GroupAffinityKey = %q, want gak:conv_generic", context.GroupAffinityKey)
	}
	if derives, stickies := affinity.counts(); derives == 0 || stickies == 0 {
		t.Fatalf("text 对照组必须查询组亲和: derive=%d sticky=%d", derives, stickies)
	}
	if context.UsageContext.GroupID != "grp_b" {
		t.Fatalf("text 对照组落组 = %q, want grp_b（黏住命中）", context.UsageContext.GroupID)
	}
}

// TestMediaDispatchPreflightIgnoresSeededGroupAffinityKey：媒体域跨组回退重入
// 携带的组亲和种子键一并丢弃（设计 §5 不变量——键恒空，不透传给写回消费）。
func TestMediaDispatchPreflightIgnoresSeededGroupAffinityKey(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{}}
	service, _ := mediaDomainPreflightService(t, affinity, &fakeSessionAffinity{})

	options := plainPreflightOptions()
	options.DispatchStaticResolution = true
	options.GroupAffinityKey = "gak:conv_generic"
	options.RequestLane = gatewayproto.RequestLane("video")
	context, _ := groupStickyPreflight(t, service, options)
	if context.GroupAffinityKey != "" {
		t.Fatalf("媒体域种子键必须丢弃: %q", context.GroupAffinityKey)
	}
	if derives, stickies := affinity.counts(); derives != 0 || stickies != 0 {
		t.Fatalf("媒体域不得查询组亲和: derive=%d sticky=%d", derives, stickies)
	}
}
