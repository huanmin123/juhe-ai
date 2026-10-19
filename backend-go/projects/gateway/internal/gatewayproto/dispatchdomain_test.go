package gatewayproto

import "testing"

// T10 调度域判定词表（对话与非对话媒体调度分离设计 §1/§5）：lane → 域映射
// 是各消费面（preflight / 准入门 / 候选准备 / 失败派发器 / 派发循环）共用的
// 唯一权威源，词表漂移即跨面行为分裂。
func TestDispatchDomainOfLane(t *testing.T) {
	cases := []struct {
		lane  RequestLane
		want  DispatchDomain
		label string
	}{
		{LaneText, DispatchDomainConversation, "文本对话走 ConversationDispatch"},
		{LaneImage, DispatchDomainStatelessMedia, "图像创建走 StatelessMediaDispatch"},
		{LaneAudio, DispatchDomainStatelessMedia, "同步音频创建走 StatelessMediaDispatch"},
		{LaneVideo, DispatchDomainStatelessMedia, "视频/长音频创建走 StatelessMediaDispatch"},
		{LaneRealtime, DispatchDomainRealtime, "Realtime 是连接生命周期能力，不归入媒体创建域"},
		{RequestLane(""), DispatchDomainConversation, "空 lane（解析前兜底形态）保持对话语义"},
		{RequestLane("unknown"), DispatchDomainConversation, "未知 lane 保持对话语义"},
	}
	for _, testCase := range cases {
		if got := DispatchDomainOfLane(testCase.lane); got != testCase.want {
			t.Fatalf("%s: DispatchDomainOfLane(%q) = %q, want %q", testCase.label, testCase.lane, got, testCase.want)
		}
	}
}
