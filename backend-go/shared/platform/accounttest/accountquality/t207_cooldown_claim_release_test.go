package accountquality

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// releaseRecordingMutation 在 mockCooldownMutation 之上记录 ReleaseProbeClaim
// 调用（T207 C6：复测异常路径释放本次 claim）。
type releaseRecordingMutation struct {
	mockCooldownMutation
	mu         sync.Mutex
	calls      []string // "accountID|keyFingerprint|claimToken"
	releaseErr error
}

func (m *releaseRecordingMutation) ReleaseProbeClaim(_ context.Context, accountID, keyFingerprint, claimToken string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, accountID+"|"+keyFingerprint+"|"+claimToken)
	return true, m.releaseErr
}

func (m *releaseRecordingMutation) releaseCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func releaseTestRunner(logger *fakeLogger, prober Prober, mutation CooldownMutation) *CooldownRetestRunner {
	return NewCooldownRetestRunner(CooldownDeps{
		Logger: logger, Reader: cooldownReader(), Prober: prober, Mutation: mutation,
		Settings: func(string, int, int) int { return 24 }, Concurrency: func() int { return 2 }, QueueWorkers: 1,
	})
}

// TestCooldownProbeErrorReleasesClaim 锁定 T207 C6 异常路径一：探针调用异常
// 时在返回前释放本次 claim（token 取自候选携带值），返回错误语义不变。
func TestCooldownProbeErrorReleasesClaim(t *testing.T) {
	mutation := &releaseRecordingMutation{}
	runner := releaseTestRunner(&fakeLogger{}, &mockProber{err: errors.New("t207 probe failure")}, mutation)
	item := CooldownQueueItem{Candidate: cooldownCandidate("acc-1", "fp-1", "sk-key"), MaxRecoveryHours: 24}
	done, err := runner.runQueueItem(context.Background(), QueueRunContext{}, item)
	if done {
		t.Fatal("探针异常必须保持既有重试语义（done=false）")
	}
	if err == nil || !strings.Contains(err.Error(), "复测存在调用异常") {
		t.Fatalf("探针异常返回原错误语义: %v", err)
	}
	calls := mutation.releaseCalls()
	if len(calls) != 1 || calls[0] != "acc-1|fp-1|token-1" {
		t.Fatalf("异常路径必须释放本进程 claim: %v", calls)
	}
}

// TestCooldownMissingObservationReleasesClaim 锁定 T207 C6 异常路径二：无诊断
// 结果（missing diagnostic result）时释放本次 claim，返回语义保持 (true, nil)。
func TestCooldownMissingObservationReleasesClaim(t *testing.T) {
	mutation := &releaseRecordingMutation{}
	runner := releaseTestRunner(&fakeLogger{}, &mockProber{}, mutation)
	item := CooldownQueueItem{Candidate: cooldownCandidate("acc-1", "fp-1", "sk-key"), MaxRecoveryHours: 24}
	done, err := runner.runQueueItem(context.Background(), QueueRunContext{}, item)
	if !done || err != nil {
		t.Fatalf("无诊断结果保持既有 (true, nil) 语义: done=%t err=%v", done, err)
	}
	calls := mutation.releaseCalls()
	if len(calls) != 1 || calls[0] != "acc-1|fp-1|token-1" {
		t.Fatalf("无诊断结果路径必须释放本进程 claim: %v", calls)
	}
}

// TestCooldownClaimReleaseFailureKeepsSemantics 锁定释放失败臂：释放错误只
// 告警（claim_release_failed 事件），不改变两处异常路径的原有返回语义。
func TestCooldownClaimReleaseFailureKeepsSemantics(t *testing.T) {
	logger := &fakeLogger{}
	mutation := &releaseRecordingMutation{releaseErr: errors.New("t207 release failure")}
	t.Run("探针异常", func(t *testing.T) {
		runner := releaseTestRunner(logger, &mockProber{err: errors.New("t207 probe failure")}, mutation)
		item := CooldownQueueItem{Candidate: cooldownCandidate("acc-1", "fp-1", "sk-key"), MaxRecoveryHours: 24}
		done, err := runner.runQueueItem(context.Background(), QueueRunContext{}, item)
		if done || err == nil {
			t.Fatalf("释放失败不得改变探针异常返回语义: done=%t err=%v", done, err)
		}
	})
	t.Run("诊断结果缺失", func(t *testing.T) {
		runner := releaseTestRunner(logger, &mockProber{}, mutation)
		item := CooldownQueueItem{Candidate: cooldownCandidate("acc-1", "fp-1", "sk-key"), MaxRecoveryHours: 24}
		done, err := runner.runQueueItem(context.Background(), QueueRunContext{}, item)
		if !done || err != nil {
			t.Fatalf("释放失败不得改变无诊断结果返回语义: done=%t err=%v", done, err)
		}
	})
	if logger.findByEvent("background_account_api_key_cooldown_retest_claim_release_failed") == nil {
		t.Fatal("释放失败必须落 claim_release_failed 告警事件")
	}
}

// TestCooldownReleaseSkipsWithoutClaimToken 锁定空 token guard：候选未携带
// claim token 时不得发起释放调用。
func TestCooldownReleaseSkipsWithoutClaimToken(t *testing.T) {
	mutation := &releaseRecordingMutation{}
	runner := releaseTestRunner(&fakeLogger{}, &mockProber{}, mutation)
	candidate := cooldownCandidate("acc-1", "fp-1", "sk-key")
	candidate.ProbeClaimToken = ""
	runner.releaseProbeClaimBestEffort(context.Background(), candidate)
	if calls := mutation.releaseCalls(); len(calls) != 0 {
		t.Fatalf("空 token 不得触发释放: %v", calls)
	}
}
