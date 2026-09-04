package retention

import (
	"context"
	"errors"
	"testing"
)

func TestExpiredDeletedAccountMissingResult(t *testing.T) {
	logger, buffer := newTestLogger()
	db := &fakeDbService{} // summary nil
	job := &ExpiredDeletedAccountJob{DB: db, Clock: fixedClock(fixedNow()), Logger: logger}
	err := job.Run(context.Background())
	if err == nil || err.Error() != "DB service 未返回逻辑删除 AI 账户清理结果" {
		t.Fatalf("error = %v, want the missing-result message", err)
	}
	assertLogContains(t, buffer,
		"background_expired_deleted_account_cleanup_failed",
		"超过一个月的逻辑删除 AI 账户物理清理失败",
	)
}

func TestExpiredDeletedAccountErrorPropagatesAndLogs(t *testing.T) {
	logger, buffer := newTestLogger()
	db := &fakeDbService{expiredErr: errors.New("business db down")}
	job := &ExpiredDeletedAccountJob{DB: db, Clock: fixedClock(fixedNow()), Logger: logger}
	if err := job.Run(context.Background()); err == nil || err.Error() != "business db down" {
		t.Fatalf("error = %v, want business db down", err)
	}
	assertLogContains(t, buffer, "background_expired_deleted_account_cleanup_failed")
}

// TestExpiredDeletedAccountEnqueueTargets mirrors the Node flow: every
// recordCleanupTarget becomes an account_related_cleanup enqueue; a dropped
// enqueue warns with the dropped reason; the summary log fires only when
// attempted>0 or orphanedAuthorizationInstances>0.
func TestExpiredDeletedAccountEnqueueTargets(t *testing.T) {
	tests := []struct {
		name           string
		summary        *ExpiredDeletedAccountSummary
		enqueuerResult EnqueueResult
		wantJobs       int
		wantWarn       bool
		wantSummaryLog bool
	}{
		{
			name: "targets enqueue and summary logs",
			summary: &ExpiredDeletedAccountSummary{
				Attempted:                      2,
				Completed:                      2,
				OrphanedAuthorizationInstances: 0,
				RecordCleanupTargets: []ExpiredDeletedAccountTarget{
					{AccountID: "acc-1", SystemAccountID: "sys-1", RelatedAccountIDs: []string{"rel-1"}, AuthorizationIDs: []string{"auth-1"}, TeamScopeIDs: []string{"team-1"}},
					{AccountID: "acc-2", SystemAccountID: "sys-2"},
				},
			},
			wantJobs:       2,
			wantSummaryLog: true,
		},
		{
			name: "dropped enqueue warns with reason",
			summary: &ExpiredDeletedAccountSummary{
				Attempted:            1,
				RecordCleanupTargets: []ExpiredDeletedAccountTarget{{AccountID: "acc-1", SystemAccountID: "sys-1"}},
			},
			enqueuerResult: EnqueueResult{Queued: false, DroppedReason: "worker_local_queue_full"},
			wantJobs:       1,
			wantWarn:       true,
			wantSummaryLog: true,
		},
		{
			name:           "empty summary stays silent",
			summary:        &ExpiredDeletedAccountSummary{},
			wantJobs:       0,
			wantSummaryLog: false,
		},
		{
			name:           "orphaned authorizations trigger summary log",
			summary:        &ExpiredDeletedAccountSummary{OrphanedAuthorizationInstances: 3},
			wantJobs:       0,
			wantSummaryLog: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			db := &fakeDbService{expiredSummary: tt.summary}
			enqueuer := &fakeEnqueuer{result: tt.enqueuerResult}
			job := &ExpiredDeletedAccountJob{DB: db, Enqueuer: enqueuer, Clock: fixedClock(fixedNow()), Logger: logger}
			if err := job.Run(context.Background()); err != nil {
				t.Fatalf("Run() unexpected error: %v", err)
			}
			if len(enqueuer.jobs) != tt.wantJobs {
				t.Fatalf("enqueued jobs = %d, want %d", len(enqueuer.jobs), tt.wantJobs)
			}
			for _, enqueued := range enqueuer.jobs {
				if enqueued.Type != JobTypeAccountRelatedCleanup {
					t.Fatalf("enqueued type = %q, want account_related_cleanup", enqueued.Type)
				}
				if enqueued.ID == "" || enqueued.CreatedAt == "" {
					t.Fatalf("enqueued job not normalized: %+v", enqueued)
				}
			}
			if tt.wantJobs > 0 {
				first := enqueuer.jobs[0]
				if first.AccountID != "acc-1" || first.SystemAccountID != "sys-1" {
					t.Fatalf("target fields not forwarded: %+v", first)
				}
			}
			if tt.wantWarn {
				assertLogContains(t, buffer,
					"background_expired_deleted_account_record_cleanup_enqueue_failed",
					"逻辑删除 AI 账户物理清理发现关联记录未清空，投递记录清理失败",
					"worker_local_queue_full",
				)
			}
			if tt.wantSummaryLog {
				assertLogContains(t, buffer,
					"background_expired_deleted_account_cleanup_completed",
					"逻辑删除 AI 账户物理清理与孤儿授权实例扫尾完成",
				)
			}
			if !tt.wantWarn && !tt.wantSummaryLog && buffer.String() != "" {
				t.Fatalf("silent run must not log:\n%s", buffer.String())
			}
		})
	}
}
