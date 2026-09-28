package main

// 可靠性批次2（缺陷1）组合根测试：composeChatFamily 把 GenerationHub 的生成
// 排空注册进 composed.shutdowns——执行停机钩子后 hub 进入停机态，新 runner
// 注册被拒绝（此前 compose.go 注释宣称 "chat generation hub drain first" 但
// 从未接线）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func TestComposeChatFamilyRegistersHubDrainBatch2(t *testing.T) {
	db := w1hNewChatFamilyDB(t)
	composed := &composition{db: db, kernel: kernel.New(kernel.Options{}), authDeps: &authsys.Deps{}}
	services := &chainRuntimeServices{Cache: w1hNewRuntimeCache(t, &w1hReadModels{})}
	deps, err := composeChatFamily(composed, runtimeConfig{ChatAssetsRoot: t.TempDir(), Secret: "batch2-secret"}, db, services, &gatewayChain{}, w1hAccountLookup{}, w1hAccountOptionsLookup{})
	if err != nil {
		t.Fatalf("composeChatFamily = %v", err)
	}
	if deps == nil || deps.Hub == nil {
		t.Fatalf("deps / hub 缺失")
	}
	if len(composed.shutdowns) == 0 {
		t.Fatalf("composeChatFamily 应注册停机钩子（hub 排空）")
	}
	// 执行全部停机钩子：hub drain 是唯一注册项，空注册表下立即返回。
	for _, shutdown := range composed.shutdowns {
		shutdown()
	}
	// 停机态断言：新 runner 注册被拒绝（hub 已 shuttingDown）。
	runnerCtx, runnerCancel := context.WithCancel(context.Background())
	defer runnerCancel()
	runner := chat.NewChatGenerationRunner(chat.ChatGenerationRunnerOptions{
		Identity: chat.ChatGenerationIdentity{OwnerID: "owner", ConversationID: "conv-batch2", TurnID: "turn-1"},
		Execute: func(ctx *chat.ChatGenerationExecutionContext) (chat.ChatGenerationTerminalResult, error) {
			return chat.ChatGenerationTerminalResult{Status: "completed"}, nil
		},
	}, runnerCtx, runnerCancel, func() bool { return runnerCtx.Err() != nil })
	if deps.Hub.Register(runner) {
		t.Fatalf("停机后 hub 不应再接受新 runner")
	}
}
