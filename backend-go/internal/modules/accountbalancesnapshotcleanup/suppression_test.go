package accountbalancesnapshotcleanup

import (
	"testing"
	"time"
)

func TestIsSuppressedUntilMatchingNewerSnapshotExists(t *testing.T) {
	cutoff := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		current SuppressionRead
		want    bool
	}{
		{name: "missing snapshot", want: true},
		{
			name: "different refresh generation",
			current: SuppressionRead{
				ConfigurationNextRefreshAt: "2026-07-20T09:00:00Z",
				SnapshotNextRefreshAfter:   "2026-07-20T10:00:00Z",
				SnapshotUpdatedAt:          cutoff.Add(time.Second),
				HasSnapshot:                true,
			},
			want: true,
		},
		{
			name: "matching snapshot at cutoff",
			current: SuppressionRead{
				ConfigurationNextRefreshAt: "2026-07-20T09:00:00Z",
				SnapshotNextRefreshAfter:   "2026-07-20T09:00:00Z",
				SnapshotUpdatedAt:          cutoff,
				HasSnapshot:                true,
			},
			want: true,
		},
		{
			name: "matching snapshot newer than cutoff",
			current: SuppressionRead{
				ConfigurationNextRefreshAt: "2026-07-20T09:00:00Z",
				SnapshotNextRefreshAfter:   "2026-07-20T09:00:00Z",
				SnapshotUpdatedAt:          cutoff.Add(time.Nanosecond),
				HasSnapshot:                true,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSuppressed(cutoff, tt.current); got != tt.want {
				t.Fatalf("IsSuppressed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSuppressedTreatsEmptyRefreshGenerationAsMatching(t *testing.T) {
	cutoff := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	current := SuppressionRead{
		SnapshotUpdatedAt: cutoff.Add(time.Second),
		HasSnapshot:       true,
	}
	if IsSuppressed(cutoff, current) {
		t.Fatal("IsSuppressed() = true, want false for matching empty refresh generation")
	}
}
