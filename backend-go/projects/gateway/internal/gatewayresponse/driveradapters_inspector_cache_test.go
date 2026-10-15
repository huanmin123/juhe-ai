package gatewayresponse

import (
	"reflect"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 双快照消除的语义回归：适配器缓存 PushChunk/PushText/Finish 产出的快照，
// Snapshot() 在两次变更之间复用。缓存必须与内层实时快照逐字段等价——包括
// 首次变更之前（构造期以初始快照起步，零值 StreamInspection 与初始状态不
// 等价）、连续变更之后，以及重复 Snapshot() 不改动状态。
func TestAnthropicInspectorAdapterSnapshotMatchesInner(t *testing.T) {
	adapter := newAnthropicInspectorAdapter()
	assertAdapterSnapshotMatchesInner(t, "anthropic 初始", adapter.Snapshot(),
		convertAnthropicInspection(adapter.inner.Snapshot()))

	adapter.PushChunk([]byte("event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
	assertAdapterSnapshotMatchesInner(t, "anthropic PushChunk 后", adapter.Snapshot(),
		convertAnthropicInspection(adapter.inner.Snapshot()))

	adapter.PushText("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	assertAdapterSnapshotMatchesInner(t, "anthropic PushText 后", adapter.Snapshot(),
		convertAnthropicInspection(adapter.inner.Snapshot()))

	// 重复取快照不改动状态：两次结果必须相同（缓存复用，不是实时重算）。
	assertAdapterSnapshotMatchesInner(t, "anthropic 重复 Snapshot", adapter.Snapshot(), adapter.Snapshot())

	adapter.Finish()
	assertAdapterSnapshotMatchesInner(t, "anthropic Finish 后", adapter.Snapshot(),
		convertAnthropicInspection(adapter.inner.Snapshot()))
}

func TestGeminiInspectorAdapterSnapshotMatchesInner(t *testing.T) {
	adapter := newGeminiInspectorAdapter()
	assertAdapterSnapshotMatchesInner(t, "gemini 初始", adapter.Snapshot(),
		convertGeminiInspection(adapter.inner.Snapshot()))

	adapter.PushChunk([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"))
	assertAdapterSnapshotMatchesInner(t, "gemini PushChunk 后", adapter.Snapshot(),
		convertGeminiInspection(adapter.inner.Snapshot()))

	adapter.PushText("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"there\"}]}}]}\n\n")
	assertAdapterSnapshotMatchesInner(t, "gemini PushText 后", adapter.Snapshot(),
		convertGeminiInspection(adapter.inner.Snapshot()))

	assertAdapterSnapshotMatchesInner(t, "gemini 重复 Snapshot", adapter.Snapshot(), adapter.Snapshot())

	adapter.Finish()
	assertAdapterSnapshotMatchesInner(t, "gemini Finish 后", adapter.Snapshot(),
		convertGeminiInspection(adapter.inner.Snapshot()))
}

func assertAdapterSnapshotMatchesInner(t *testing.T, label string, got, want gatewayproto.StreamInspection) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s 快照与内层不一致\n got=%+v\nwant=%+v", label, got, want)
	}
}
