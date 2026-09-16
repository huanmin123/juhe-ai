package main

// w11g 覆盖补充：桥接流在上游 EOF 前缺少终止事件时的中断收尾闭包
//（Fail*FromChatStream 族）与未映射模型的回退。

import (
	"io"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// 无终止事件的 Chat SSE（无 [DONE]）。
const w11gChatUpstreamSSEInterrupted = "data: {\"id\":\"cmpl-w11g\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"He\"}}]}\n\n"

// 无 message_stop 的 Anthropic SSE。
const w11gAnthropicUpstreamSSEInterrupted = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_w11g\",\"model\":\"m\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"He\"}}\n\n"

// 无 finishReason 的 Gemini SSE。
const w11gGeminiUpstreamSSEInterrupted = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"He\"}]}}],\"modelVersion\":\"m\"}\n\n"

func TestW11GBridgeInterruptedStreamFinishes(t *testing.T) {
	transformer := newChainBridgeResponseTransformer()
	read := func(input gatewaydispatch.UpstreamResponseTransformInput) string {
		got, err := transformer.TransformUpstreamResponseForAccount(input)
		if err != nil {
			t.Fatalf("transform: %v", err)
		}
		payload, readErr := io.ReadAll(got.Body)
		if readErr != nil {
			t.Fatalf("read: %v", readErr)
		}
		return string(payload)
	}
	// Chat 上游中断：Anthropic / Gemini / Responses 客户端。
	for name, source := range map[string]string{
		"messages":         "messages",
		"generate_content": "generate_content",
		"responses":        "responses",
	} {
		input := w11gBridgeInput(t, source, newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gChatUpstreamSSEInterrupted))))
		if payload := read(input); !strings.Contains(payload, "data:") {
			t.Fatalf("chat interrupted %s payload=%q", name, payload)
		}
	}
	// Anthropic 上游中断：Gemini / Responses / Chat 客户端。
	for _, source := range []string{"generate_content", "responses", "chat_completions"} {
		input := w11gBridgeInput(t, source, newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gAnthropicUpstreamSSEInterrupted))))
		input.Account = w11gMappingAccount(source, "messages")
		if payload := read(input); !strings.Contains(payload, "data:") {
			t.Fatalf("anthropic interrupted %s payload=%q", source, payload)
		}
	}
	// Gemini 上游中断：Chat / Anthropic / Responses 客户端。
	for _, source := range []string{"chat_completions", "messages", "responses"} {
		input := w11gBridgeInput(t, source, newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gGeminiUpstreamSSEInterrupted))))
		input.Account = w11gMappingAccount(source, "generate_content")
		if payload := read(input); !strings.Contains(payload, "data:") {
			t.Fatalf("gemini interrupted %s payload=%q", source, payload)
		}
	}
}


func TestW11GBridgeUpstreamModelFallback(t *testing.T) {
	// 映射行缺 UpstreamModel：回退到请求 model（76-79）。
	enabled := true
	account := w11gMappingAccount("messages", "chat_completions")
	account.ModelMappings[0].UpstreamModel = ""
	_ = enabled
	input := w11gBridgeInput(t, "messages", newBridgeUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(w11gChatUpstreamSSE))))
	input.Account = account
	transformer := newChainBridgeResponseTransformer()
	got, err := transformer.TransformUpstreamResponseForAccount(input)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	payload, readErr := io.ReadAll(got.Body)
	if readErr != nil || !strings.Contains(string(payload), "data:") {
		t.Fatalf("payload=%q err=%v", payload, readErr)
	}
}
