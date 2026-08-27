package modelcheckprobe

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

type BasicProbeInput struct {
	Endpoint         string
	Protocol         modelcheckprofile.Protocol
	Model            string
	Prompt           string
	Stream           bool
	MaxOutputTokens  int
	Headers          http.Header
	Client           *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
	ModelLimit       int
	CountTokens      func(string) int
}

// RunBasicProbe is the first jobs-owned network-to-score composition. It does
// not persist, retry, or call another process; those policies belong to the
// future input executor.
func RunBasicProbe(ctx context.Context, input BasicProbeInput) (EvaluationItem, error) {
	request, err := BuildBasic(input.Protocol, input.Model, input.Prompt, BasicOptions{MaxOutputTokens: input.MaxOutputTokens, Stream: input.Stream})
	if err != nil {
		return EvaluationItem{}, err
	}
	result, transportErr := Execute(ctx, request, TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes})
	return evaluateBasicResult(result, transportErr, input), nil
}

// RunBasicProbeWithRetry uses the same composition as RunBasicProbe but lets
// the caller provide the jobs retry schedule and cancellation-aware delay.
func RunBasicProbeWithRetry(ctx context.Context, input BasicProbeInput, retry RetryOptions) (EvaluationItem, error) {
	request, err := BuildBasic(input.Protocol, input.Model, input.Prompt, BasicOptions{MaxOutputTokens: input.MaxOutputTokens, Stream: input.Stream})
	if err != nil {
		return EvaluationItem{}, err
	}
	result, transportErr := ExecuteWithRetry(ctx, request, TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}, retry)
	return evaluateBasicResult(result, transportErr, input), nil
}

func RunStructuredProbe(ctx context.Context, input BasicProbeInput, retry RetryOptions) (EvaluationItem, error) {
	request, err := BuildStructured(input.Protocol, input.Model, input.Stream)
	if err != nil {
		return EvaluationItem{}, err
	}
	result, transportErr := ExecuteWithRetry(ctx, request, TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}, retry)
	if transportErr != nil && result.Response.ErrorMessage == "" {
		result.Response.ErrorMessage = "模型检测 transport 失败"
	}
	return EvaluateStructuredOutput(result, input.Model, "target"), nil
}

func RunToolProbe(ctx context.Context, input BasicProbeInput, retry RetryOptions) (EvaluationItem, error) {
	request, err := BuildTool(input.Protocol, input.Model, input.Stream)
	if err != nil {
		return EvaluationItem{}, err
	}
	result, transportErr := ExecuteWithRetry(ctx, request, TransportOptions{Endpoint: input.Endpoint, Headers: input.Headers, Client: input.Client, Timeout: input.Timeout, MaxResponseBytes: input.MaxResponseBytes}, retry)
	if transportErr != nil && result.Response.ErrorMessage == "" {
		result.Response.ErrorMessage = "模型检测 transport 失败"
	}
	return EvaluateToolCalling(result, input.Model, "target"), nil
}

func evaluateBasicResult(result ProbeResult, transportErr error, input BasicProbeInput) EvaluationItem {
	if transportErr != nil && result.Response.ErrorMessage == "" {
		result.Response.ErrorMessage = fmt.Sprintf("模型检测 transport 失败：%v", transportErr)
	}
	prefix := "target"
	itemType := "responses_basic"
	if input.Protocol == modelcheckprofile.ProtocolOpenAIChat {
		itemType = "chat_basic"
	} else if input.Protocol == modelcheckprofile.ProtocolAnthropic {
		itemType = "anthropic_basic"
	} else if input.Protocol == modelcheckprofile.ProtocolGeminiNative {
		itemType = "gemini_basic"
	}
	if input.Stream {
		return EvaluateProtocolStream(result, input.Model, ProtocolEvaluationOptions{ItemKey: prefix + "." + itemType + "_stream", ItemType: itemType + "_stream", SuccessMessage: "模型检测流式调用可用", FailurePrefix: "模型检测流式调用失败"})
	}
	return EvaluateBasicProtocol(result, input.Model, ProtocolEvaluationOptions{ItemKey: prefix + "." + itemType, ItemType: itemType, SuccessMessage: "模型检测基础调用可用", FailurePrefix: "模型检测基础调用失败"})
}
