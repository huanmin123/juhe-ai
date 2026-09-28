package accounthealth

// BUG-0218 回归：J1 显式探活请求（outbox drain 与 runCycle requests 循环）
// 对候选构造失败的账户无隔离——LoadAccount 抛错使行保持 pending（毒丸行）
// 且 runCycle 整轮中断，单坏账户停摆全链探活（生产 2026-09-28：16 行卡死
// J1 达 35 分钟）。修复后：实现 LoadAccountWithFailures 的 reader 构造失败
// 返回 Failures，调用方保持零值 input 走 runExplicitRequest 的 input_stale
// 终态结算，行按已处理收敛出队。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// probeDrainFailingAccountReader 模拟单账户候选构造失败：老接口抛错（缺陷
// 形态文案），隔离接口返回 Failures。
type probeDrainFailingAccountReader struct{}

func (probeDrainFailingAccountReader) LoadDue(context.Context, int) ([]Input, error) { return nil, nil }

func (probeDrainFailingAccountReader) LoadAccount(_ context.Context, accountID string) ([]Input, error) {
	return nil, fmt.Errorf("PG direct input account=%s 候选构造失败；请使用 LoadAccountWithFailures 处理隔离结果", accountID)
}

func (probeDrainFailingAccountReader) LoadAccountWithFailures(_ context.Context, accountID string) (DirectInputLoadResult, error) {
	return DirectInputLoadResult{
		Inputs:   []Input{},
		Failures: []DirectInputFailure{{AccountID: accountID, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}},
	}, nil
}

// TestDrainProbeOutboxCandidateFailureConsumesRow：构造失败行不得保持
// pending（缺陷形态：drain 返回 err、行每周期重试永不消费），必须按
// input_stale 收敛出队且 drain 整体不返回错误。
func TestDrainProbeOutboxCandidateFailureConsumesRow(t *testing.T) {
	outbox := &probeOutboxMemoryStore{pending: []ProbeOutboxRow{
		{
			RequestID: "j1-poison-candidate",
			AccountID: "account-1",
			Reason:    "request_failure",
			Deadline:  time.Now().UTC().Add(time.Minute),
		},
		{
			RequestID: "j1-healthy-follower",
			AccountID: "account-2",
			Reason:    "request_failure",
			Deadline:  time.Now().UTC().Add(time.Minute),
		},
	}}
	healthy := testInput("http://127.0.0.1:9", "chat_json")
	healthy.AccountID = "account-2"
	// 单 reader：account-1 构造失败、account-2 正常返回（两行都走隔离接口）。
	reader := &splitAccountReader{failures: map[string]bool{"account-1": true}, inputs: map[string][]Input{"account-2": {healthy}}}
	runner := newDrainRunner(t, "drain-secret", outbox, probeDrainBoundary{
		facts: map[string][3]int64{"account-1": {1, 1, 1}, "account-2": {1, 1, 1}},
		ok:    map[string]bool{"account-1": true, "account-2": true},
	}, reader)
	lease := drainLease(t, runner)

	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("构造失败行不得使 drain 返回错误: %v", err)
	}
	if outbox.claimCount() != 0 || len(outbox.consumed) != 2 {
		t.Fatalf("两行都必须收敛出队: pending=%d consumed=%v", outbox.claimCount(), outbox.consumed)
	}
}

// splitAccountReader 按 accountID 分流：failures 中的账户返回隔离失败，
// 其余返回预置 inputs。
type splitAccountReader struct {
	failures map[string]bool
	inputs   map[string][]Input
}

func (r *splitAccountReader) LoadDue(context.Context, int) ([]Input, error) { return nil, nil }

func (r *splitAccountReader) LoadAccount(ctx context.Context, accountID string) ([]Input, error) {
	result, err := r.LoadAccountWithFailures(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(result.Failures) > 0 {
		return nil, fmt.Errorf("PG direct input account=%s 候选构造失败；请使用 LoadAccountWithFailures 处理隔离结果", accountID)
	}
	return result.Inputs, nil
}

func (r *splitAccountReader) LoadAccountWithFailures(_ context.Context, accountID string) (DirectInputLoadResult, error) {
	if r.failures[accountID] {
		return DirectInputLoadResult{
			Inputs:   []Input{},
			Failures: []DirectInputFailure{{AccountID: accountID, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}},
		}, nil
	}
	return DirectInputLoadResult{Inputs: r.inputs[accountID]}, nil
}
