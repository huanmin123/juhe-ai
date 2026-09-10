package chat

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// 内部工具注册表/编排器与图片生成传输是工具往返的核心：参数校验、去重缓存、
// 失败纠错、错误码归类和 b64 解码契约必须与 Node 一致。执行器与资产接收器
// 全部用 Mock 替换，不触达真实上游。

type stubImageGenerationW3 struct {
	calls int
	fail  error
	result ChatImageGenerationToolResult
}

func (s *stubImageGenerationW3) generate(request ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
	s.calls++
	if s.fail != nil {
		return ChatImageGenerationToolResult{}, s.fail
	}
	return s.result, nil
}

var testTinyPNGBytesW3 = mustDecodeBase64W3(testTinyPNGBase64)

func mustDecodeBase64W3(value string) []byte {
	decoded, err := decodeBase64Payload(value, 1<<20)
	if err != nil {
		panic(err)
	}
	return decoded
}

func toolContextW3(image *stubImageGenerationW3) *chatToolExecutionContext {
	return &chatToolExecutionContext{
		DefaultImageModel: "gpt-image-2",
		ImageGeneration: func(request ChatImageGenerationRequest) (ChatImageGenerationToolResult, error) {
			return image.generate(request)
		},
		ArtifactSink: stubSinkW3{},
	}
}

type stubSinkW3 struct{ calls int }

func (s stubSinkW3) CommitGeneratedImage(input GeneratedImageCommitInput) (GeneratedImageCommitResult, error) {
	return GeneratedImageCommitResult{AssetID: "asset-1", MimeType: input.Result.MimeType, Width: input.Result.Width, Height: input.Result.Height, Bytes: input.Result.Bytes}, nil
}

// TestChatInternalToolRegistryW3 覆盖注册表的装配与参数归一。
func TestChatInternalToolRegistryW3(t *testing.T) {
	dev := newChatInternalToolRegistry("development", true, false)
	if _, ok := dev.definitions["diagnostic_echo"]; !ok {
		t.Fatalf("development 环境应有 diagnostic_echo")
	}
	if _, ok := dev.definitions["generate_image"]; ok {
		t.Fatalf("图片未开通时不应有 generate_image")
	}
	imageOn := newChatInternalToolRegistry("production", false, true)
	if _, ok := imageOn.definitions["diagnostic_echo"]; ok {
		t.Fatalf("production 不应有 diagnostic_echo")
	}
	if _, ok := imageOn.definitions["generate_image"]; !ok {
		t.Fatalf("图片开通后应有 generate_image")
	}
	if tools := dev.resolveTools(false); len(tools) != 0 {
		t.Fatalf("function calling 关闭时应返回空: %d", len(tools))
	}
	if tools := dev.resolveTools(true); len(tools) != 1 || tools[0].ModelName != "diagnostic_echo" {
		t.Fatalf("resolveTools 应只含可用工具: %v", tools)
	}
	if _, err := dev.definition("bogus"); err == nil {
		t.Fatalf("未知工具应报错")
	}
	if _, err := dev.normalizeArguments("diagnostic_echo", strings.Repeat("a", 5000), 4096); err == nil {
		t.Fatalf("超限参数应报错")
	}
	if _, err := dev.normalizeArguments("diagnostic_echo", "{bad", 4096); err == nil {
		t.Fatalf("非法 JSON 应报错")
	}
	if _, err := dev.normalizeArguments("diagnostic_echo", "[1]", 4096); err == nil {
		t.Fatalf("非对象根值应报错")
	}
	value, err := dev.normalizeArguments("diagnostic_echo", `{"text":"hi"}`, 4096)
	if err != nil || value["text"] != "hi" {
		t.Fatalf("合法参数解析失败: %v %v", value, err)
	}
	if chatToolErrorMessage("bogus") != "工具执行失败" || chatToolErrorMessage("tool_timeout") != "工具执行超时" {
		t.Fatalf("chatToolErrorMessage 契约不正确")
	}
}

// TestDiagnosticEchoToolW3 覆盖回显工具的执行契约。
func TestDiagnosticEchoToolW3(t *testing.T) {
	tool := newDiagnosticEchoTool()
	result, err := tool.Execute(map[string]any{"text": "ping"}, nil)
	if err != nil || result.PublicResult["echoedText"] != "ping" {
		t.Fatalf("回显失败: %+v err=%v", result, err)
	}
	nonString, err := tool.Execute(map[string]any{"text": 42}, nil)
	if err != nil || nonString.PublicResult["echoedText"] != "42" {
		t.Fatalf("非字符串应 fmt.Sprint: %+v", nonString)
	}
	empty, err := tool.Execute(nil, nil)
	if err != nil || empty.PublicResult["echoedText"] != "" {
		t.Fatalf("空输入应回显空: %+v", empty)
	}
}

// TestExecuteGenerateImageToolW3 覆盖图片工具的参数分支与成功路径。
func TestExecuteGenerateImageToolW3(t *testing.T) {
	image := &stubImageGenerationW3{result: ChatImageGenerationToolResult{Data: testTinyPNGBytesW3, Bytes: int64(len(testTinyPNGBytesW3)), MimeType: "image/png", Width: 1, Height: 1}}
	context := toolContextW3(image)

	t.Run("运行时未配置", func(t *testing.T) {
		if _, err := executeGenerateImageTool(map[string]any{"prompt": "x"}, &chatToolExecutionContext{}); err == nil {
			t.Fatalf("未配置运行时应报错")
		}
	})
	t.Run("提示词为空", func(t *testing.T) {
		if _, err := executeGenerateImageTool(map[string]any{}, context); err == nil || !strings.Contains(err.Error(), "提示词不能为空") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("引用 ID 非法", func(t *testing.T) {
		_, err := executeGenerateImageTool(map[string]any{"prompt": "p", "reference_asset_ids": []any{"bad-id"}}, context)
		if err == nil || !strings.Contains(err.Error(), "assetId 无效") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("操作类型无效", func(t *testing.T) {
		_, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": "bogus"}, context)
		if err == nil || !strings.Contains(err.Error(), "操作类型无效") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("edit 缺引用", func(t *testing.T) {
		_, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": "edit"}, context)
		if err == nil || !strings.Contains(err.Error(), "至少引用一张来源图片") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("generate 携带引用", func(t *testing.T) {
		_, err := executeGenerateImageTool(map[string]any{"prompt": "p", "action": "generate", "reference_asset_ids": []any{"chat_asset_" + strings.Repeat("a", 32)}}, context)
		if err == nil || !strings.Contains(err.Error(), "生成图片不能携带来源图片") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("模型不受支持", func(t *testing.T) {
		_, err := executeGenerateImageTool(map[string]any{"prompt": "p", "model": "dall-e-9"}, context)
		if err == nil || !strings.Contains(err.Error(), "不受支持") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("参数归一失败", func(t *testing.T) {
		if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "size": "100x100"}, context); err == nil {
			t.Fatalf("非 16 倍数尺寸应报错")
		}
		if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "quality": "ultra"}, context); err == nil {
			t.Fatalf("非法质量应报错")
		}
		if _, err := executeGenerateImageTool(map[string]any{"prompt": "p", "output_format": "gif"}, context); err == nil {
			t.Fatalf("非法输出格式应报错")
		}
	})
	t.Run("成功生成", func(t *testing.T) {
		result, err := executeGenerateImageTool(map[string]any{"prompt": " 一只猫 ", "size": "auto", "quality": "high"}, context)
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if result.PublicResult["assetId"] != "asset-1" || result.PublicResult["operation"] != "generate" || result.PublicResult["size"] != "auto" {
			t.Fatalf("公共结果不正确: %v", result.PublicResult)
		}
		if image.calls != 1 {
			t.Fatalf("图片生成应调用一次: %d", image.calls)
		}
	})
	t.Run("auto + 引用即编辑", func(t *testing.T) {
		editContext := toolContextW3(&stubImageGenerationW3{result: ChatImageGenerationToolResult{Data: testTinyPNGBytesW3, Bytes: int64(len(testTinyPNGBytesW3)), MimeType: "image/png", Width: 1, Height: 1}})
		loaded := false
		editContext.LoadImageEditReferences = func(assetIDs []string) ([]ChatImageEditReference, error) {
			loaded = true
			return []ChatImageEditReference{{AssetID: assetIDs[0], Data: testTinyPNGBytesW3, Bytes: int64(len(testTinyPNGBytesW3)), MimeType: "image/png", Filename: "a.png"}}, nil
		}
		result, err := executeGenerateImageTool(map[string]any{"prompt": "p", "reference_asset_ids": []any{"chat_asset_" + strings.Repeat("b", 32)}}, editContext)
		if err != nil || result.PublicResult["operation"] != "edit" || !loaded {
			t.Fatalf("auto+引用应走编辑: %+v err=%v", result.PublicResult, err)
		}
	})
}

// TestNormalizeChatImagePolicyW3 覆盖尺寸/质量/格式归一与引用 ID 校验。
func TestNormalizeChatImagePolicyW3(t *testing.T) {
	if size, err := normalizeChatImageSize(nil); err != nil || size != "auto" {
		t.Fatalf("nil 尺寸应为 auto: %s %v", size, err)
	}
	if _, err := normalizeChatImageSize("4000x4000"); err == nil {
		t.Fatalf("超 3840 长边应报错")
	}
	if _, err := normalizeChatImageSize("3840x960"); err == nil {
		t.Fatalf("超 3:1 比例应报错")
	}
	// 恰好 3:1 允许。
	if _, err := normalizeChatImageSize("3072x1024"); err != nil {
		t.Fatalf("3:1 尺寸应允许: %v", err)
	}
	if _, err := normalizeChatImageSize("16x16"); err == nil {
		t.Fatalf("低于最小像素应报错")
	}
	if _, err := normalizeChatImageSize("99999999x16"); err == nil {
		t.Fatalf("超最大像素应报错")
	}
	if size, err := normalizeChatImageSize(" 1024X1024 "); err != nil || size != "1024x1024" {
		t.Fatalf("合法尺寸归一失败: %s %v", size, err)
	}
	if quality, err := normalizeChatImageQuality(nil); err != nil || quality != "auto" {
		t.Fatalf("nil 质量应为 auto")
	}
	if _, err := normalizeChatImageQuality("ultra"); err == nil {
		t.Fatalf("非法质量应报错")
	}
	if format, err := normalizeChatImageOutputFormat(nil); err != nil || format != "webp" {
		t.Fatalf("nil 格式应为 webp")
	}
	if _, err := normalizeChatImageOutputFormat("gif"); err == nil {
		t.Fatalf("非法格式应报错")
	}
	if _, err := normalizeReferenceAssetIDs("not-array"); err == nil {
		t.Fatalf("非数组引用应报错")
	}
	if _, err := normalizeReferenceAssetIDs([]any{1}); err == nil {
		t.Fatalf("非字符串项应归一为字符串并校验")
	}
	if _, err := normalizeReferenceAssetIDs([]any{"chat_asset_" + strings.Repeat("a", 32), "chat_asset_" + strings.Repeat("a", 32)}); err == nil {
		t.Fatalf("重复引用应报错")
	}
	if _, err := normalizeReferenceAssetIDs(make([]any, 6)); err == nil {
		t.Fatalf("超 5 个引用应报错")
	}
	if ids, err := normalizeReferenceAssetIDs(nil); err != nil || len(ids) != 0 {
		t.Fatalf("nil 引用应为空数组: %v %v", ids, err)
	}
	if err := validateChatImageEditReferenceLimits(nil); err == nil {
		t.Fatalf("空引用应报错")
	}
	if err := validateChatImageEditReferenceLimits(make([]ChatImageEditReference, 6)); err == nil {
		t.Fatalf("超 5 个引用应报错")
	}
	if err := validateChatImageEditReferenceLimits([]ChatImageEditReference{{Bytes: 0}}); err == nil {
		t.Fatalf("非法字节应报错")
	}
	if err := validateChatImageEditReferenceLimits([]ChatImageEditReference{{Bytes: 1}}); err != nil {
		t.Fatalf("合法引用不应报错: %v", err)
	}
}

// TestChatInternalToolOrchestratorW3 覆盖编排器的轮次、缓存与失败纠错。
func TestChatInternalToolOrchestratorW3(t *testing.T) {
	registry := newChatInternalToolRegistry("development", true, true)

	t.Run("无工具调用直接返回", func(t *testing.T) {
		orchestrator := newChatInternalToolOrchestrator(registry, registry.resolveTools(true), &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 2, MaxToolCalls: 4, MaxImageCalls: 2}, nil)
		result, err := orchestrator.Run(ProtocolChatCompletions, func(round int, continuation []any) (ChatToolModelTurn, error) {
			if round != 1 || len(continuation) != 0 {
				t.Errorf("首轮参数不正确: %d %v", round, continuation)
			}
			return ChatToolModelTurn{Content: "回答", FinishReason: "stop", InputTokens: int64PtrT(3)}, nil
		})
		if err != nil || result.Content != "回答" || result.ModelRounds != 1 || result.InputTokens == nil {
			t.Fatalf("结果不正确: %+v err=%v", result, err)
		}
	})
	t.Run("工具轮次与精确去重", func(t *testing.T) {
		var events []ChatToolExecutionEvent
		orchestrator := newChatInternalToolOrchestrator(registry, registry.resolveTools(true), &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 4, MaxImageCalls: 2}, func(event ChatToolExecutionEvent) {
			events = append(events, event)
		})
		round := 0
		result, err := orchestrator.Run(ProtocolChatCompletions, func(r int, continuation []any) (ChatToolModelTurn, error) {
			round++
			if r == 1 {
				return ChatToolModelTurn{ToolCalls: []ChatToolCall{
					{CallID: "c2", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"hi"}`, SourceOrder: 1},
					{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"hi"}`, SourceOrder: 0},
				}}, nil
			}
			if len(continuation) != 2 {
				t.Errorf("续答项目数量 = %d, 期望 2", len(continuation))
			}
			return ChatToolModelTurn{Content: "完成"}, nil
		})
		if err != nil || result.ToolCalls != 2 || result.ModelRounds != 2 || round != 2 {
			t.Fatalf("结果不正确: %+v err=%v round=%d", result, err, round)
		}
		// SourceOrder 排序后 c1 先执行；相同参数第二次命中复用。
		reused := false
		for _, event := range events {
			if event.Reused {
				reused = true
			}
		}
		if !reused {
			t.Fatalf("相同参数应命中 reuse_exact: %+v", events)
		}
	})
	t.Run("取消传播", func(t *testing.T) {
		aborted := false
		context := &chatToolExecutionContext{Aborted: func() bool { return aborted }}
		orchestrator := newChatInternalToolOrchestrator(registry, registry.resolveTools(true), context, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 4, MaxImageCalls: 2}, nil)
		_, err := orchestrator.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			aborted = true
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"x"}`}}}, nil
		})
		if err == nil || err.Error() != "工具执行已取消" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("工具失败纠错一次", func(t *testing.T) {
		var events []ChatToolExecutionEvent
		emptyRegistry := &chatInternalToolRegistry{definitions: map[string]*toolDefinition{}}
		orchestrator := newChatInternalToolOrchestrator(emptyRegistry, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 4, MaxImageCalls: 2}, func(event ChatToolExecutionEvent) {
			events = append(events, event)
		})
		result, err := orchestrator.Run(ProtocolChatCompletions, func(r int, continuation []any) (ChatToolModelTurn, error) {
			if r == 1 {
				return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "missing_tool", ArgumentsJSON: `{}`}}}, nil
			}
			if len(continuation) != 1 {
				t.Errorf("失败载荷应进入续答: %d", len(continuation))
			}
			return ChatToolModelTurn{Content: " recovered"}, nil
		})
		if err != nil {
			t.Fatalf("可纠正失败不应中断: %v", err)
		}
		if len(events) != 2 || events[0].Status != "started" || events[1].Status != "failed" {
			t.Fatalf("事件序列不正确: %+v", events)
		}
		if events[1].ErrorCode != "tool_not_available" {
			t.Fatalf("错误码不正确: %s", events[1].ErrorCode)
		}
		if !strings.Contains(result.Content, "recovered") {
			t.Fatalf("应继续完成: %+v", result)
		}
	})
	t.Run("第二次失败直接失败", func(t *testing.T) {
		emptyRegistry := &chatInternalToolRegistry{definitions: map[string]*toolDefinition{}}
		orchestrator := newChatInternalToolOrchestrator(emptyRegistry, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 4, MaxToolCalls: 9, MaxImageCalls: 2}, nil)
		_, err := orchestrator.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "missing_tool", ArgumentsJSON: `{}`}}}, nil
		})
		if err == nil {
			t.Fatalf("第二次失败应直接报错")
		}
	})
	t.Run("轮次与次数上限", func(t *testing.T) {
		limits := newChatInternalToolOrchestrator(registry, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 1, MaxToolCalls: 4, MaxImageCalls: 2}, nil)
		if _, err := limits.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{}`}}}, nil
		}); err == nil {
			t.Fatalf("轮次上限应报错")
		}
		fewCalls := newChatInternalToolOrchestrator(registry, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 0, MaxImageCalls: 2}, nil)
		_, err := fewCalls.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{"text":"x"}`}}}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "tool_call_limit_exceeded") == false {
			t.Fatalf("工具次数上限应报错: %v", err)
		}
	})
	t.Run("中断错误不进入纠错", func(t *testing.T) {
		abortError := &abortTestErrorW3{}
		failing := &toolDefinition{ID: "t", Version: "1", ModelName: "diagnostic_echo", MaxArgumentBytes: 1024, MaxResultBytes: 1024, TimeoutMs: 1, DuplicatePolicy: "allow_repeat", Execute: func(map[string]any, *chatToolExecutionContext) (chatToolExecutionResult, error) {
			return chatToolExecutionResult{}, abortError
		}}
		registryOne := &chatInternalToolRegistry{definitions: map[string]*toolDefinition{"diagnostic_echo": failing}}
		orchestrator := newChatInternalToolOrchestrator(registryOne, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 4, MaxImageCalls: 2}, nil)
		_, err := orchestrator.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{}`}}}, nil
		})
		if err != abortError {
			t.Fatalf("中断错误应原样透传: %v", err)
		}
	})
	t.Run("panic 向外传播", func(t *testing.T) {
		// 契约：编排器自身不 recover，panic 由 buildGenerationExecute 的
		// defer/recover 包装层统一转为内部错误。
		panicking := &toolDefinition{ID: "t", Version: "1", ModelName: "diagnostic_echo", MaxArgumentBytes: 1024, MaxResultBytes: 1024, TimeoutMs: 1, DuplicatePolicy: "allow_repeat", Execute: func(map[string]any, *chatToolExecutionContext) (chatToolExecutionResult, error) {
			panic("工具崩溃")
		}}
		registryOne := &chatInternalToolRegistry{definitions: map[string]*toolDefinition{"diagnostic_echo": panicking}}
		orchestrator := newChatInternalToolOrchestrator(registryOne, nil, &chatToolExecutionContext{}, ChatOrchestratorLimits{MaxModelRounds: 3, MaxToolCalls: 4, MaxImageCalls: 2}, nil)
		defer func() {
			recovered := recover()
			if recovered != "工具崩溃" {
				t.Fatalf("panic 应原样透传: %v", recovered)
			}
		}()
		_, _ = orchestrator.Run(ProtocolChatCompletions, func(int, []any) (ChatToolModelTurn, error) {
			return ChatToolModelTurn{ToolCalls: []ChatToolCall{{CallID: "c1", ToolName: "diagnostic_echo", ArgumentsJSON: `{}`}}}, nil
		})
		t.Fatalf("应发生 panic")
	})
}

type abortTestErrorW3 struct{}

func (e *abortTestErrorW3) Error() string   { return "aborted" }
func (e *abortTestErrorW3) AbortError() bool { return true }

// TestGenerateChatImageW3 用 Mock 执行器覆盖图片生成传输的完整链路。
func TestGenerateChatImageW3(t *testing.T) {
	t.Run("入参校验", func(t *testing.T) {
		if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Prompt: "p"}, "key", ""); err == nil {
			t.Fatalf("空模型应报错")
		}
		if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "gpt-image-2"}, "key", ""); err == nil {
			t.Fatalf("空提示词应报错")
		}
		if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", Size: "10x10"}, "key", ""); err == nil {
			t.Fatalf("非法尺寸应报错")
		}
		if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", Quality: "ultra"}, "key", ""); err == nil {
			t.Fatalf("非法质量应报错")
		}
		if _, err := GenerateChatImage(context.Background(), nil, ChatImageGenerationRequest{Model: "m", Prompt: "p", OutputFormat: "gif"}, "key", ""); err == nil {
			t.Fatalf("非法格式应报错")
		}
	})
	t.Run("生成成功并提取修订提示词", func(t *testing.T) {
		body := `{"data":[{"b64_json":"` + testTinyPNGBase64 + `"}],"revised_prompt":"一只猫"}`
		var dispatched dispatchCall
		executor := mockExecutor{steps: []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/images/generations" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				dispatched = call
				return jsonStatusResponse(200, body)
			},
		}}}
		result, err := GenerateChatImage(context.Background(), &executor, ChatImageGenerationRequest{Model: " gpt-image-2 ", Prompt: " 猫 "}, "secret", "trace-1")
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if result.MimeType != "image/png" || result.Width != 1 || result.Height != 1 || result.RevisedPrompt != "一只猫" {
			t.Fatalf("结果不正确: %+v", result)
		}
		if !strings.Contains(dispatched.Body, `"prompt":"猫"`) || !strings.Contains(dispatched.Body, `"quality":"auto"`) {
			t.Fatalf("请求体不正确: %s", dispatched.Body)
		}
		if dispatched.Headers["x-trace-id"] != "trace-1" || dispatched.Headers["authorization"] != "Bearer secret" {
			t.Fatalf("请求头不正确: %v", dispatched.Headers)
		}
	})
	t.Run("编辑走 multipart", func(t *testing.T) {
		executor := mockExecutor{steps: []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/images/edits" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				if !strings.Contains(call.Body, `name="image[]"`) || !strings.Contains(call.Headers["content-type"], "multipart/form-data") {
					t.Errorf("编辑请求应为 multipart: %s", call.Body)
				}
				return jsonStatusResponse(200, `{"data":[{"b64_json":"`+testTinyPNGBase64+`"}]}`)
			},
		}}}
		_, err := GenerateChatImage(context.Background(), &executor, ChatImageGenerationRequest{
			Model: "m", Prompt: "p", References: []ChatImageEditReference{{Data: testTinyPNGBytesW3, Bytes: int64(len(testTinyPNGBytesW3)), Filename: "a.png"}},
		}, "key", "")
		if err != nil {
			t.Fatalf("编辑失败: %v", err)
		}
	})
	t.Run("上游失败映射", func(t *testing.T) {
		cases := []struct {
			status int
			body   string
			code   PublicChatGenerationErrorCode
		}{
			{403, `{"error":{"message":"image generation is not enabled for the group","type":"permission_error"}}`, GenErrImageNotEnabled},
			{401, `{"error":{"message":"bad key","type":"invalid_request_error"}}`, GenErrImagePermissionDenied},
			{429, `{"error":{"message":"slow down"}}`, GenErrImageRateLimited},
			{400, `{"error":{"message":"bad size"}}`, GenErrImageRequestRejected},
			{500, `{}`, GenErrImageFailed},
		}
		for _, testCase := range cases {
			executor := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
				return jsonStatusResponse(testCase.status, testCase.body)
			}}}}
			_, err := GenerateChatImage(context.Background(), &executor, ChatImageGenerationRequest{Model: "m", Prompt: "p"}, "key", "")
			var imageErr *ChatImageGenerationRequestError
			if !errorsAs(err, &imageErr) {
				t.Fatalf("status %d 应返回图片请求错误: %v", testCase.status, err)
			}
			if imageErr.Code != testCase.code {
				t.Fatalf("status %d code = %s, 期望 %s", testCase.status, imageErr.Code, testCase.code)
			}
			if imageErr.StatusCode != testCase.status {
				t.Fatalf("statusCode = %d", imageErr.StatusCode)
			}
		}
	})
	t.Run("响应载荷异常", func(t *testing.T) {
		executor := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(200, `{"data":[]}`)
		}}}}
		if _, err := GenerateChatImage(context.Background(), &executor, ChatImageGenerationRequest{Model: "m", Prompt: "p"}, "key", ""); err == nil || !strings.Contains(err.Error(), "缺少 b64_json") {
			t.Fatalf("缺 b64_json 应报错: %v", err)
		}
		badBase64 := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(200, `{"data":[{"b64_json":"!!!"}]}`)
		}}}}
		if _, err := GenerateChatImage(context.Background(), &badBase64, ChatImageGenerationRequest{Model: "m", Prompt: "p"}, "key", ""); err == nil || !strings.Contains(err.Error(), "无法解码") {
			t.Fatalf("非法 base64 应报错: %v", err)
		}
		unknownBytes := mockExecutor{steps: []scriptStep{{respond: func(dispatchCall) *GenerationDispatchResponse {
			return jsonStatusResponse(200, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString([]byte("hello"))+`"}]}`)
		}}}}
		if _, err := GenerateChatImage(context.Background(), &unknownBytes, ChatImageGenerationRequest{Model: "m", Prompt: "p"}, "key", ""); err == nil {
			t.Fatalf("未知图片字节应报错")
		}
	})
	t.Run("执行器错误", func(t *testing.T) {
		executor := failingExecutorW3{}
		if _, err := GenerateChatImage(context.Background(), executor, ChatImageGenerationRequest{Model: "m", Prompt: "p"}, "key", ""); !errors.Is(err, errExecutorFailedW3) {
			t.Fatalf("执行器错误应透传: %v", err)
		}
	})
}

type failingExecutorW3 struct{}

var errExecutorFailedW3 = errors.New("dispatch 失败")

func (failingExecutorW3) Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error) {
	return nil, errExecutorFailedW3
}

// TestImageBytesW3 覆盖 MIME 嗅探与尺寸解析。
func TestImageBytesW3(t *testing.T) {
	if got := imageMimeTypeFromBytes(testTinyPNGBytesW3); got != "image/png" {
		t.Fatalf("PNG 嗅探失败: %s", got)
	}
	// 完整 JPEG 段结构：SOI + APP0(长度16) + SOF0（高 0x20 宽 0x40）。
	jpeg := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xC0, 0x00, 0x11, 0x08, 0x00, 0x20, 0x00, 0x40, 0x00,
	}
	if got := imageMimeTypeFromBytes(jpeg); got != "image/jpeg" {
		t.Fatalf("JPEG 嗅探失败: %s", got)
	}
	width, height := imageDimensionsFromBytes(jpeg)
	if width != 64 || height != 32 {
		t.Fatalf("JPEG 尺寸 = %dx%d", width, height)
	}
	if got := imageMimeTypeFromBytes([]byte("plain text")); got != "" {
		t.Fatalf("未知字节应返回空 MIME: %s", got)
	}
	if width, height := imageDimensionsFromBytes([]byte("short")); width != 0 || height != 0 {
		t.Fatalf("未知字节尺寸应为 0")
	}
	// WebP VP8 简单损耗格式。
	webp := make([]byte, 30)
	copy(webp[0:4], "RIFF")
	copy(webp[8:12], "WEBP")
	copy(webp[12:16], "VP8 ")
	if got := imageMimeTypeFromBytes(webp); got != "image/webp" {
		t.Fatalf("WebP 嗅探失败: %s", got)
	}
	copy(webp[12:16], "VP8L")
	if _, _ = imageDimensionsFromBytes(webp); false {
		t.Fatalf("占位")
	}
	copy(webp[12:16], "VP8X")
	webp[24] = 15
	webp[27] = 15
	if width, height := webpDimensions(webp); width != 16 || height != 16 {
		t.Fatalf("VP8X 尺寸 = %dx%d", width, height)
	}
	copy(webp[12:16], "XXXX")
	if width, height := webpDimensions(webp); width != 0 || height != 0 {
		t.Fatalf("未知 chunk 尺寸应为 0")
	}
	if width, height := webpDimensions(nil); width != 0 || height != 0 {
		t.Fatalf("短数据应为 0")
	}
}

// TestStorageKeysAndObjectStoreW3 覆盖存储键派生与本地对象存储契约。
func TestStorageKeysAndObjectStoreW3(t *testing.T) {
	key := StorageKeyForChatAsset("chat_asset_1", strings.Repeat("A", 64), "image/png", "preview")
	if !strings.HasPrefix(key, "aa/aa/") || !strings.HasSuffix(key, "-preview-aaaaaaaaaaaaaaaa.png") {
		t.Fatalf("存储键不正确: %s", key)
	}
	if got := chatAssetObjectExtension("image/jpeg"); got != ".jpg" {
		t.Fatalf("jpeg 扩展名不正确: %s", got)
	}
	if got := chatAssetObjectExtension("bogus"); got != ".bin" {
		t.Fatalf("未知 MIME 扩展名不正确: %s", got)
	}
	// 路径分隔符被清洗为下划线；目录穿越由 LocalObjectStore.path 兜底。
	if got := safeStorageSegment("../evil", 120); strings.Contains(got, "/") || got != ".._evil" {
		t.Fatalf("路径段应被清洗: %q", got)
	}
	if got := safeStorageSegment("", 10); got != "_" {
		t.Fatalf("空段应为下划线: %q", got)
	}
	if got := normalizedSHA256(strings.ToUpper(strings.Repeat("a", 64))); got != strings.Repeat("a", 64) {
		t.Fatalf("哈希应小写归一: %s", got)
	}
	if got := normalizedSHA256("short"); got != strings.Repeat("0", 64) {
		t.Fatalf("非法哈希应替换为全 0: %s", got)
	}

	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("创建对象存储失败: %v", err)
	}
	data := []byte("图片数据")
	digest := hexEncode(mustSum256W3(data))
	if err := store.Write("aa/bb/object.bin", data, 100, digest); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := store.Write("aa/bb/object.bin", data, 100, "wrong"); err == nil {
		t.Fatalf("哈希不一致应报错")
	}
	if err := store.Write("aa/bb/object.bin", make([]byte, 101), 100, ""); err == nil {
		t.Fatalf("超限应报错")
	}
	if err := store.Write("aa/bb/object.bin", nil, 100, ""); err == nil {
		t.Fatalf("空数据应报错")
	}
	if err := store.Write("../escape", data, 100, ""); err == nil {
		t.Fatalf("越界键应报错")
	}
	read, size, err := store.Open("aa/bb/object.bin", 100)
	if err != nil || size != int64(len(data)) || string(read) != string(data) {
		t.Fatalf("读取失败: %v %d", err, size)
	}
	if _, _, err := store.Open("aa/bb/missing.bin", 100); err == nil {
		t.Fatalf("缺失文件应报错")
	}
	if _, _, err := store.Open("aa/bb/object.bin", 2); err == nil {
		t.Fatalf("超读取上限应报错")
	}
	if _, _, err := store.Open("../escape", 100); err == nil {
		t.Fatalf("越界读取应报错")
	}
	if err := store.Delete([]string{"aa/bb/object.bin", "", "../escape"}); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, _, err := store.Open("aa/bb/object.bin", 100); err == nil {
		t.Fatalf("删除后读取应报错")
	}
}

func mustSum256W3(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
