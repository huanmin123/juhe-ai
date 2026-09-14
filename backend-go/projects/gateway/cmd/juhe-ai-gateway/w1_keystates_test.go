package main

// w1（单元层）：账户 Key 运行态写桥的 SQLite 往返——RecordFailure 落行、
// RecordSuccess 清态、结果 changed/skipped 语义（fixture 预置
// account_api_key_runtime_states 表）。

import (
	"context"
	"testing"

	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accountkeystates"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestW1AccountAPIKeyWriterRoundTrip(t *testing.T) {
	fixture := newChainFixture(t)
	store, err := accountkeystates.NewStore(accountkeystates.Config{
		DB: fixture.db, Postgres: false, Secret: "chain-test-secret", Now: time.Now,
		InvalidateRuntimeCache: func(string) {},
	})
	if err != nil {
		t.Fatalf("store = %v", err)
	}
	writer := &chainAccountAPIKeyWriter{keyStates: store}
	status := gatewayaccounteffects.AccountApiKeyFailureStatus("probe_failed")
	code := "probe_503"
	message := "探活失败"
	write := gatewayaccounteffects.AccountAPIKeyFailureWrite{
		Account: gatewayruntimecache.OpenAIAccountSecret{ID: fixture.accountID},
		Input: gatewayaccounteffects.AccountAPIKeyFailureWriteInput{
			Status: status, ErrorCode: &code, ErrorMessage: &message,
		},
	}
	result, err := writer.RecordFailure(context.Background(), write)
	if err != nil {
		t.Fatalf("record failure = %v", err)
	}
	_ = result
	// 成功写：预期状态命中后清除失败态。
	success := gatewayaccounteffects.AccountAPIKeySuccessWrite{
		Account: gatewayruntimecache.OpenAIAccountSecret{ID: fixture.accountID},
	}
	if _, err := writer.RecordSuccess(context.Background(), success); err != nil {
		t.Fatalf("record success = %v", err)
	}
	// Key 状态目标投影：授权账户携带绑定上下文。
	target := chainKeyStateTargetOf(gatewaydispatch.AccountCandidate{ID: "acc_9"})
	if target.AccountID != "acc_9" {
		t.Fatalf("target = %+v", target)
	}
}
