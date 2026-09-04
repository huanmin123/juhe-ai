package retention

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestChatRetentionInputContract asserts the request mirrors the Node DB
// service payload byte for byte: now, now-20min interruption window, limit
// 1000, retentionDays, lease fence and the low-priority lane.
func TestChatRetentionInputContract(t *testing.T) {
	now := fixedNow()
	db := &fakeDbService{chatResult: &ChatRetentionResult{}}
	lease := &ScheduledLeaseFence{LeaseKey: "chat-retention-cleanup", OwnerID: "owner-1", FencingToken: 7}
	job := &ChatRetentionJob{DB: db, Clock: fixedClock(now), RetentionDays: 3, Lease: lease}
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if len(db.chatInputs) != 1 {
		t.Fatalf("chat inputs = %d, want 1", len(db.chatInputs))
	}
	input := db.chatInputs[0]
	if input.Now != "2026-09-04T20:30:00.000Z" {
		t.Fatalf("now = %q", input.Now)
	}
	if input.InterruptedBefore != "2026-09-04T20:10:00.000Z" {
		t.Fatalf("interruptedBefore = %q, want now-20min", input.InterruptedBefore)
	}
	if input.Limit != ChatRetentionLimit {
		t.Fatalf("limit = %d, want %d", input.Limit, ChatRetentionLimit)
	}
	if input.RetentionDays != 3 {
		t.Fatalf("retentionDays = %d, want 3", input.RetentionDays)
	}
	if input.ScheduledLease != lease {
		t.Fatalf("lease fence not forwarded: %+v", input.ScheduledLease)
	}
	if input.Priority != "low" {
		t.Fatalf("priority = %q, want low", input.Priority)
	}
}

func TestChatRetentionMissingResultError(t *testing.T) {
	db := &fakeDbService{} // chatResult stays nil: transport-level miss
	job := &ChatRetentionJob{DB: db, Clock: fixedClock(fixedNow()), RetentionDays: 3}
	err := job.Run(context.Background())
	if err == nil || err.Error() != "DB service 未返回 AI 问答保留清理结果" {
		t.Fatalf("error = %v, want the missing-result message", err)
	}
}

func TestChatRetentionDbErrorPropagates(t *testing.T) {
	db := &fakeDbService{chatErr: errors.New("db service down")}
	job := &ChatRetentionJob{DB: db, Clock: fixedClock(fixedNow()), RetentionDays: 3}
	if err := job.Run(context.Background()); err == nil || err.Error() != "db service down" {
		t.Fatalf("error = %v, want db service down", err)
	}
}

// TestChatRetentionCompletionLogGate mirrors the Node condition: the info log
// fires when any of droppedPartitions/deletedMessages/deletedConversations/
// recoveredTurns/recoveredCompactions/deletedCheckpoints/claimedAssets/
// failedAssets is positive, never on the empty result.
func TestChatRetentionCompletionLogGate(t *testing.T) {
	tests := []struct {
		name    string
		result  ChatRetentionResult
		wantLog bool
	}{
		{name: "empty result stays silent", result: ChatRetentionResult{}, wantLog: false},
		{name: "only hasMore stays silent", result: ChatRetentionResult{HasMore: true, HasMoreAssets: true, HasMoreCheckpoints: true}, wantLog: false},
		{name: "deletedAssets alone stays silent", result: ChatRetentionResult{DeletedAssets: 5}, wantLog: false},
		{name: "deletedMessages triggers", result: ChatRetentionResult{DeletedMessages: 1}, wantLog: true},
		{name: "recoveredCompactions triggers", result: ChatRetentionResult{RecoveredCompactions: 2}, wantLog: true},
		{name: "failedAssets triggers", result: ChatRetentionResult{FailedAssets: 1}, wantLog: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			db := &fakeDbService{chatResult: &tt.result}
			job := &ChatRetentionJob{DB: db, Clock: fixedClock(fixedNow()), RetentionDays: 3, Logger: logger}
			if err := job.Run(context.Background()); err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}
			contents := buffer.String()
			if tt.wantLog {
				assertLogContains(t, buffer, "chat_retention_cleanup_completed", "AI 问答过期数据清理与中断轮次恢复完成", "deletedMessages")
				if contents == "" {
					t.Fatal("expected a completion log")
				}
				return
			}
			if contents != "" {
				t.Fatalf("silent run must not log, got:\n%s", contents)
			}
		})
	}
}

// TestChatRetentionDeadline asserts the 60s DB service timeout is applied.
func TestChatRetentionDeadline(t *testing.T) {
	var gotDeadline time.Duration
	db := &deadlineCapturingDb{onCall: func(d time.Duration) { gotDeadline = d }, result: &ChatRetentionResult{}}
	job := &ChatRetentionJob{DB: db, Clock: fixedClock(fixedNow()), RetentionDays: 3}
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if gotDeadline > ChatRetentionDbServiceTimeout {
		t.Fatalf("deadline %v exceeds the Node 60s timeout", gotDeadline)
	}
	if gotDeadline <= 0 {
		t.Fatal("no deadline set")
	}
}

type deadlineCapturingDb struct {
	onCall func(time.Duration)
	result *ChatRetentionResult
}

func (d *deadlineCapturingDb) CleanupChatRetention(ctx context.Context, _ ChatRetentionInput) (*ChatRetentionResult, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		d.onCall(time.Until(deadline))
	}
	return d.result, nil
}

func (d *deadlineCapturingDb) CleanupExpiredSystemSessions(context.Context, string, int) (int64, error) {
	return 0, nil
}

func (d *deadlineCapturingDb) CleanupExpiredCodexContextStates(context.Context, string, int) (*CodexContextExpiredCleanup, error) {
	return nil, nil
}

func (d *deadlineCapturingDb) SettleCodexContextStorageCleanup(context.Context, CodexContextSettlement) (CodexContextSettlementResult, error) {
	return CodexContextSettlementResult{}, nil
}

func (d *deadlineCapturingDb) CleanupExpiredDeletedAccounts(context.Context) (*ExpiredDeletedAccountSummary, error) {
	return nil, nil
}
