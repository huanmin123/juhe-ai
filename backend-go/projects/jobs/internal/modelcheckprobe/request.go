// Package modelcheckprobe contains the pure J3b protocol request contracts.
// It intentionally constructs bytes only; dialing, credentials, retries, and
// persistence stay in the future jobs runner.
package modelcheckprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

const systemInstruction = "You are a model capability checker. Follow the requested output exactly."

type BasicOptions struct {
	MaxOutputTokens int
	Stream          bool
	Temperature     *float64
}

type Request struct {
	Path          string
	Protocol      modelcheckprofile.Protocol
	ExpectedModel string
	Body          json.RawMessage
}

// BuildBasic creates the frozen base capability probe request for one
// provider protocol. The returned Body has no credentials and may be sent as
// is by the jobs-owned transport.
func BuildBasic(protocol modelcheckprofile.Protocol, model, prompt string, options BasicOptions) (Request, error) {
	if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt) == "" || options.MaxOutputTokens <= 0 {
		return Request{}, errors.New("model check basic probe input is invalid")
	}
	temperature := 0.0
	if options.Temperature != nil {
		temperature = *options.Temperature
	}
	var path string
	var payload any
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		path = "/v1/responses"
		payload = map[string]any{
			"model":             model,
			"input":             []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}},
			"instructions":      systemInstruction,
			"max_output_tokens": options.MaxOutputTokens,
			"stream":            options.Stream,
			"store":             false,
			"temperature":       temperature,
		}
	case modelcheckprofile.ProtocolOpenAIChat:
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model": model,
			"messages": []any{
				map[string]any{"role": "system", "content": systemInstruction},
				map[string]any{"role": "user", "content": prompt},
			},
			"max_tokens":  max(options.MaxOutputTokens, 64),
			"stream":      options.Stream,
			"temperature": temperature,
		}
	case modelcheckprofile.ProtocolAnthropic:
		path = "/v1/messages"
		payload = map[string]any{
			"model":      model,
			"system":     systemInstruction,
			"messages":   []any{map[string]any{"role": "user", "content": prompt}},
			"max_tokens": options.MaxOutputTokens,
			"stream":     options.Stream,
		}
	case modelcheckprofile.ProtocolGeminiNative:
		action := "generateContent"
		if options.Stream {
			action = "streamGenerateContent"
		}
		path = "/v1beta/models/" + url.PathEscape(model) + ":" + action
		if options.Stream {
			path += "?alt=sse"
		}
		payload = map[string]any{
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": systemInstruction}}},
			"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
			"generationConfig":  map[string]any{"temperature": temperature, "maxOutputTokens": max(options.MaxOutputTokens, 128)},
		}
	default:
		return Request{}, fmt.Errorf("unsupported model check protocol: %s", protocol)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, fmt.Errorf("marshal model check %s probe: %w", protocol, err)
	}
	return Request{Path: path, Protocol: protocol, ExpectedModel: model, Body: body}, nil
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
