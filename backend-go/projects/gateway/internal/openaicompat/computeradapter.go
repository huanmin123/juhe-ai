package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ComputerAdapter ports openai-compatible-computer/computer-adapter.ts: the
// configured HTTP browser adapter executor for OpenAI-hosted computer-use
// tools. Node gates it on hostedToolRuntimes.computer === 'local_runtime'
// plus computerAdapter.enabled + endpoint; the executor override replaces the
// Node set-openAICompatibleComputerExecutorForTest hook.

// ComputerCallResult mirrors OpenAIToAnthropicComputerCallResult.
type ComputerCallResult struct {
	CallID   string
	Status   string
	Actions  []map[string]any
	Metadata map[string]any
}

// ComputerRuntimeResult mirrors OpenAIToAnthropicComputerRuntimeResult.
type ComputerRuntimeResult struct {
	Message  string
	Call     *ComputerCallResult
	Metadata map[string]any
}

// ComputerRuntimeInput mirrors OpenAIToAnthropicComputerRuntimeInput.
type ComputerRuntimeInput struct {
	Body   map[string]any
	Tool   map[string]any
	Stream bool
}

// ComputerExecutor mirrors OpenAIToAnthropicComputerExecutor.
type ComputerExecutor interface {
	Run(ctx context.Context, input ComputerRuntimeInput) (*ComputerRuntimeResult, error)
}

type httpComputerExecutor struct {
	config ComputerAdapterConfig
	client HTTPDoer
}

// ComputerExecutorForRequest mirrors
// openAICompatibleComputerExecutorForGatewayRequest: nil unless the hosted
// tool runtime mode is local_runtime and the adapter is fully configured.
// override replaces the executor wholesale (the Node test hook).
func ComputerExecutorForRequest(config Config, client HTTPDoer, override ComputerExecutor) ComputerExecutor {
	if override != nil {
		return override
	}
	config = config.withDefaults()
	if config.HostedToolComputerMode != "local_runtime" {
		return nil
	}
	if !config.ComputerAdapter.Enabled || config.ComputerAdapter.Endpoint == "" {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &httpComputerExecutor{config: config.ComputerAdapter, client: client}
}

// Run mirrors runConfiguredComputerHttpAdapter.
func (e *httpComputerExecutor) Run(ctx context.Context, input ComputerRuntimeInput) (*ComputerRuntimeResult, error) {
	endpoint := e.config.Endpoint
	if endpoint == "" {
		return nil, fmt.Errorf("Computer browser adapter endpoint is not configured")
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(e.config.TimeoutMs)*time.Millisecond)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"body":   input.Body,
		"tool":   input.Tool,
		"stream": input.Stream,
	})
	if err != nil {
		return nil, computerTimeoutOrError(ctx, err)
	}
	request, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, computerTimeoutOrError(ctx, err)
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/json")
	response, err := e.client.Do(request)
	if err != nil {
		if timeoutCtx.Err() != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("Computer browser adapter request timed out")
		}
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}()
	text, err := readAdapterText(response, e.config.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("Computer browser adapter HTTP %d: %s", response.StatusCode, truncateString(text, 512))
	}
	var parsed any
	if jsonErr := json.Unmarshal([]byte(text), &parsed); jsonErr != nil {
		return nil, fmt.Errorf("Computer browser adapter response is not valid JSON")
	}
	return normalizeComputerAdapterResult(parsed)
}

// computerTimeoutOrError mirrors the abort timedOut branch.
func computerTimeoutOrError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("Computer browser adapter request timed out")
	}
	return err
}

// readAdapterText mirrors readResponseTextWithLimit in the adapter module.
func readAdapterText(response *http.Response, maxBodyBytes int64) (string, error) {
	if response.Body == nil {
		return "", nil
	}
	buffer, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(buffer)) > maxBodyBytes {
		return "", fmt.Errorf("Computer browser adapter response body exceeded limit")
	}
	return string(buffer), nil
}

func truncateString(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// normalizeComputerAdapterResult mirrors normalizeComputerAdapterResult.
func normalizeComputerAdapterResult(value any) (*ComputerRuntimeResult, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Computer browser adapter response must be a JSON object")
	}
	message := "Computer browser adapter completed."
	if text := adapterString(record["message"]); text != nil {
		message = *text
	}
	result := &ComputerRuntimeResult{Message: message, Metadata: map[string]any{}}
	if callRecord := objectValue(record["call"]); callRecord != nil {
		call := &ComputerCallResult{Actions: []map[string]any{}}
		if callID := firstAdapterString(callRecord["call_id"], callRecord["callId"]); callID != nil {
			call.CallID = *callID
		}
		if status := adapterString(callRecord["status"]); status != nil {
			call.Status = *status
		}
		call.Actions = adapterActions(callRecord["actions"])
		if metadata := objectValue(callRecord["metadata"]); metadata != nil {
			call.Metadata = metadata
		}
		result.Call = call
	}
	if metadata := objectValue(record["metadata"]); metadata != nil {
		for key, value := range metadata {
			result.Metadata[key] = value
		}
	}
	result.Metadata["adapter"] = "http_browser"
	return result, nil
}

// adapterString mirrors stringValue in the adapter (trimmed non-empty).
func adapterString(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func firstAdapterString(values ...any) *string {
	for _, value := range values {
		if text := adapterString(value); text != nil {
			return text
		}
	}
	return nil
}

// adapterActions mirrors arrayRecordValue (objects only).
func adapterActions(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	actions := []map[string]any{}
	for _, item := range list {
		if record, ok := item.(map[string]any); ok {
			actions = append(actions, record)
		}
	}
	return actions
}
