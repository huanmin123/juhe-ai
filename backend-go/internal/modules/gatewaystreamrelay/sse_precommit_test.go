package gatewaystreamrelay

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

func TestSSEPreCommitInspectorKeepsTransportOnlyFramesPrivate(t *testing.T) {
	t.Parallel()
	for _, stream := range [][]byte{
		[]byte(": keep-alive\r\n\r\n"),
		[]byte("event: progress\nid: 1\nretry: 500\nvendor-meta: pending\n\n"),
		[]byte("data: \n\n"),
		[]byte("data:"),
	} {
		sink := &recordingSink{}
		firstDownstream, firstSemantic := 0, 0
		result, err := Relay(context.Background(), &sliceSource{chunks: [][]byte{stream}}, sink, Options{
			Limits: limitsForSSEPreCommit(1024), Inspector: NewSSEPreCommitInspector(),
			OnFirstByte: func() { firstDownstream++ }, OnFirstSemanticOutput: func() { firstSemantic++ },
		})
		if !errors.Is(err, ErrPreCommitEvidenceMissing) || result.State != StateFailedBeforeFirstByte || !result.RetryAllowed || result.TransportCommitted || result.SemanticCommitted || result.BytesWritten != 0 || sink.Len() != 0 || firstDownstream != 0 || firstSemantic != 0 {
			t.Fatalf("stream=%q result=%#v err=%v sink=%q callbacks=%d/%d", stream, result, err, sink.String(), firstDownstream, firstSemantic)
		}
	}
}

func TestSSEPreCommitInspectorFlushesFragmentedOpaqueDataOnce(t *testing.T) {
	t.Parallel()
	chunks := [][]byte{[]byte("event: vendor.progress\n"), []byte("data:"), []byte(" opaque-payload"), []byte("\n\n")}
	sink := &recordingSink{}
	firstDownstream, firstSemantic := 0, 0
	result, err := Relay(context.Background(), &sliceSource{chunks: chunks}, sink, Options{
		Limits: limitsForSSEPreCommit(4096), Inspector: NewSSEPreCommitInspector(),
		OnFirstByte: func() { firstDownstream++ }, OnFirstSemanticOutput: func() { firstSemantic++ },
	})
	if err != nil || result.State != StateCompleted || !result.TransportCommitted || !result.SemanticCommitted || sink.String() != "event: vendor.progress\ndata: opaque-payload\n\n" || firstDownstream != 1 || firstSemantic != 1 {
		t.Fatalf("result=%#v err=%v sink=%q callbacks=%d/%d", result, err, sink.String(), firstDownstream, firstSemantic)
	}
}

func TestSSEPreCommitInspectorRejectsOnlyOpaqueFramingAtBound(t *testing.T) {
	t.Parallel()
	sink := &recordingSink{}
	stream := []byte("event: " + strings.Repeat("x", SSEPreCommitBufferBytes+1))
	result, err := Relay(context.Background(), &sliceSource{chunks: splitSSEChunks(stream)}, sink, Options{Limits: limitsForSSEPreCommit(int64(len(stream) + 1024)), Inspector: NewSSEPreCommitInspector()})
	if !errors.Is(err, ErrPreCommitBufferExceeded) || result.TransportCommitted || result.SemanticCommitted || result.BytesWritten != 0 || sink.Len() != 0 || result.Handoff.Usage.FailureAttribution != gatewayusage.FailureAttributionGatewayCapacity || result.Handoff.Usage.ErrorCode != "stream_precommit_buffer_exceeded" {
		t.Fatalf("result=%#v err=%v sink=%q", result, err, sink.String())
	}
}

func TestSSEPreCommitInspectorDoesNotCapVisibleData(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("v", SSEPreCommitBufferBytes+1)
	stream := []byte("data: " + payload + "\n\n")
	sink := &recordingSink{}
	result, err := Relay(context.Background(), &sliceSource{chunks: splitSSEChunks(stream)}, sink, Options{Limits: limitsForSSEPreCommit(int64(len(stream) + 1024)), Inspector: NewSSEPreCommitInspector()})
	if err != nil || !result.SemanticCommitted || sink.String() != string(stream) || !bytes.Equal([]byte(sink.String()), stream) {
		t.Fatalf("result=%#v err=%v written=%d", result, err, sink.Len())
	}
}

func limitsForSSEPreCommit(maxBytes int64) Limits {
	return Limits{MaxBytes: maxBytes, BufferBytes: 32 * 1024, IdleTimeout: time.Second, TotalTimeout: time.Second}
}

func splitSSEChunks(value []byte) [][]byte {
	const chunkSize = 32 * 1024
	chunks := make([][]byte, 0, (len(value)+chunkSize-1)/chunkSize)
	for len(value) > 0 {
		size := chunkSize
		if len(value) < size {
			size = len(value)
		}
		chunks = append(chunks, append([]byte(nil), value[:size]...))
		value = value[size:]
	}
	return chunks
}
