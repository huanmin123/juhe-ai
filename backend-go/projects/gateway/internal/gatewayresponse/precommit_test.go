package gatewayresponse

import (
	"bytes"
	"testing"
)

func TestStreamPreCommitSseEvidence(t *testing.T) {
	tests := []struct {
		name                          string
		chunks                        [][]byte
		wantDataEventObserved         bool
		wantDataPayloadStarted        bool
		wantOnlyNonSemanticFraming    bool
	}{
		{
			name:                       "仅注释帧保持私有可丢弃",
			chunks:                     [][]byte{[]byte(": keep-alive\n\n")},
			wantOnlyNonSemanticFraming: true,
		},
		{
			name:                   "data 事件触发观察",
			chunks:                 [][]byte{[]byte("data: {\"a\":1}\n\n")},
			wantDataEventObserved:  true,
			wantDataPayloadStarted: true,
		},
		{
			name:                   "data 无前导空格",
			chunks:                 [][]byte{[]byte("data:{\"a\":1}\n\n")},
			wantDataEventObserved:  true,
			wantDataPayloadStarted: true,
		},
		{
			name:                   "事件名与 data 分片",
			chunks:                 [][]byte{[]byte("event: response.output_text.delta\ndata: \"hi\"\n\n")},
			wantDataEventObserved:  true,
			wantDataPayloadStarted: true,
		},
		{
			name:                   "CRLF 跨分片",
			chunks:                 [][]byte{[]byte("data: x\r"), []byte("\n\n")},
			wantDataEventObserved:  true,
			wantDataPayloadStarted: true,
		},
		{
			name:                       "只有空 data 字段的事件可丢弃",
			chunks:                     [][]byte{[]byte("data:\n\n")},
			wantOnlyNonSemanticFraming: true,
		},
		{
			name:                   "元数据字段后跟 data",
			chunks:                 [][]byte{[]byte("id: 1\ndata: y\n\n")},
			wantDataEventObserved:  true,
			wantDataPayloadStarted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := NewStreamPreCommitSseEvidence()
			for _, chunk := range tt.chunks {
				evidence.Push(chunk)
			}
			evidence.Finish()
			if evidence.DataEventObserved != tt.wantDataEventObserved {
				t.Fatalf("DataEventObserved = %v, want %v", evidence.DataEventObserved, tt.wantDataEventObserved)
			}
			if evidence.DataPayloadStarted != tt.wantDataPayloadStarted {
				t.Fatalf("DataPayloadStarted = %v, want %v", evidence.DataPayloadStarted, tt.wantDataPayloadStarted)
			}
			if evidence.OnlyNonSemanticFramingObserved != tt.wantOnlyNonSemanticFraming {
				t.Fatalf("OnlyNonSemanticFramingObserved = %v, want %v", evidence.OnlyNonSemanticFramingObserved, tt.wantOnlyNonSemanticFraming)
			}
		})
	}
}

func TestPreCommitBufferLifecycle(t *testing.T) {
	state := NewPreCommitBufferState(true)
	inspection := PreCommitInspectionState{}
	response := PreCommitResponseState{}

	chunkA := []byte(": comment only\n")
	if !CanKeepStreamPreCommitChunk(state, inspection, chunkA, 0, response) {
		t.Fatal("first chunk should be buffered")
	}
	AppendStreamPreCommitChunk(state, chunkA)

	oversized := bytes.Repeat([]byte("x"), StreamPreCommitBufferMaxBytes)
	if !WouldExceedStreamPreCommitBuffer(state, oversized) {
		t.Fatal("oversized chunk should exceed buffer")
	}
	if CanKeepStreamPreCommitChunk(state, inspection, oversized, 0, response) {
		t.Fatal("oversized chunk must not be buffered")
	}
	if !ShouldFailBeforeStreamDownstreamCommit(state, 0, response) {
		t.Fatal("state should still fail before commit")
	}

	terminalInspection := PreCommitInspectionState{TerminalReceived: true}
	if CanKeepStreamPreCommitChunk(state, terminalInspection, []byte("x"), 0, response) {
		t.Fatal("terminal received chunk must not be buffered")
	}

	taken := TakeStreamPreCommitChunks(state)
	if len(taken) != 1 || !bytes.Equal(taken[0], chunkA) {
		t.Fatalf("taken chunks = %v", taken)
	}
	if state.Buffering {
		t.Fatal("take should disable buffering")
	}
	if UncommittedStreamResponseBody(state) != nil {
		t.Fatal("no uncommitted body after take")
	}
	AppendStreamPreCommitChunk(state, chunkA)
	state.Buffering = true
	AppendStreamPreCommitChunk(state, []byte("more"))
	uncommitted := UncommittedStreamResponseBody(state)
	if string(uncommitted) != string(chunkA)+"more" {
		t.Fatalf("uncommitted = %q", uncommitted)
	}
}

func TestResponseCanStillFailBeforeCommitGate(t *testing.T) {
	state := NewPreCommitBufferState(true)
	response := PreCommitResponseState{WritableEnded: true}
	if ShouldFailBeforeStreamDownstreamCommit(state, 0, response) {
		t.Fatal("ended response cannot fail before commit")
	}
	response = PreCommitResponseState{Destroyed: true}
	if ShouldFailBeforeStreamDownstreamCommit(state, 0, response) {
		t.Fatal("destroyed response cannot fail before commit")
	}
}
