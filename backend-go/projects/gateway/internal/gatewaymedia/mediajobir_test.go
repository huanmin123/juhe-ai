package gatewaymedia

// MediaJobIR 状态归一测试（契约 §2.6 状态归一总表 + 归一规则：未知状态值
// 一律归 in_progress 并记录原始值，不猜测失败）。
import "testing"

func TestNormalizeOpenAIStatus(t *testing.T) {
	cases := []struct {
		raw      string
		want     MediaJobStatus
		known    bool
	}{
		{"queued", JobStatusQueued, true},
		{"in_progress", JobStatusInProgress, true},
		{"completed", JobStatusCompleted, true},
		{"failed", JobStatusFailed, true},
		// 未知值：归 in_progress 且 known=false（调用方记录 RawStatus）。
		{"running", JobStatusInProgress, false},
		{"succeeded", JobStatusInProgress, false},
		{"SUCCESS", JobStatusInProgress, false}, // 大小写敏感：不归一猜测
		{"", JobStatusInProgress, false},
	}
	for _, c := range cases {
		got, known := NormalizeOpenAIStatus(c.raw)
		if got != c.want || known != c.known {
			t.Fatalf("NormalizeOpenAIStatus(%q) = (%s, %v), want (%s, %v)", c.raw, got, known, c.want, c.known)
		}
	}
}

func TestMediaJobStatusTerminal(t *testing.T) {
	terminal := []MediaJobStatus{JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusExpired}
	nonTerminal := []MediaJobStatus{JobStatusQueued, JobStatusInProgress}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Fatalf("%s 应为终态", s)
		}
	}
	for _, s := range nonTerminal {
		if s.Terminal() {
			t.Fatalf("%s 不应为终态", s)
		}
	}
}
