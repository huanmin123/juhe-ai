package usagestatscontract

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCursorGoldenSafetyFenceAndAdvance(t *testing.T) {
	safeBefore := at("2026-07-22T12:00:00Z")
	cursor := Cursor{CreatedAt: at("2026-07-22T11:59:00Z"), ID: "usage-001"}
	rows := []Cursor{
		{CreatedAt: at("2026-07-22T11:59:00Z"), ID: "usage-001"},
		{CreatedAt: at("2026-07-22T11:59:00Z"), ID: "usage-002"},
		{CreatedAt: safeBefore, ID: "usage-003"},
	}

	if IsEligible(rows[0], cursor, safeBefore) {
		t.Fatal("cursor row must not be replayed")
	}
	if !IsEligible(rows[1], cursor, safeBefore) || !IsEligible(rows[2], cursor, safeBefore) {
		t.Fatal("strictly later rows through the safety fence must be eligible")
	}
	if IsEligible(Cursor{CreatedAt: safeBefore.Add(time.Nanosecond), ID: "usage-004"}, cursor, safeBefore) {
		t.Fatal("row after safety fence must not be eligible")
	}

	next, err := AdvanceCursor(cursor, rows[1:], safeBefore)
	if err != nil {
		t.Fatalf("AdvanceCursor() error = %v", err)
	}
	if got, want := next, rows[2]; got != want {
		t.Fatalf("AdvanceCursor() = %+v, want %+v", got, want)
	}

	_, err = AdvanceCursor(cursor, []Cursor{rows[2], rows[1]}, safeBefore)
	if !errors.Is(err, ErrCursorNotStrictlyIncreasing) {
		t.Fatalf("out-of-order AdvanceCursor() error = %v, want %v", err, ErrCursorNotStrictlyIncreasing)
	}
}

func TestShardWindowGoldenRotatesWithBoundedFanout(t *testing.T) {
	tests := []struct {
		name       string
		total      int
		offset     int
		batchLimit int
		want       ShardWindow
	}{
		{
			name:       "large registry caps at sixteen",
			total:      40,
			offset:     15,
			batchLimit: 100,
			want:       ShardWindow{Indexes: []int{15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30}, NextOffset: 31},
		},
		{
			name:       "end wraps to beginning",
			total:      40,
			offset:     35,
			batchLimit: 100,
			want:       ShardWindow{Indexes: []int{35, 36, 37, 38, 39, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, NextOffset: 11},
		},
		{
			name:       "small batch is also bounded",
			total:      40,
			offset:     3,
			batchLimit: 5,
			want:       ShardWindow{Indexes: []int{3, 4, 5, 6, 7}, NextOffset: 8},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PlanShardWindow(test.total, test.offset, test.batchLimit); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("PlanShardWindow() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestHotWindowGoldenUsesStatsTimezone(t *testing.T) {
	now := at("2026-04-01T00:30:00Z")
	losAngeles, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := HotWindows(now, losAngeles), []DateRange{
		{StartDate: "2026-03-31", EndDate: "2026-03-31", Days: 1},
		{StartDate: "2026-03-30", EndDate: "2026-03-30", Days: 1},
		{StartDate: "2026-03-25", EndDate: "2026-03-31", Days: 7},
		{StartDate: "2026-03-01", EndDate: "2026-03-31", Days: 31},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HotWindows(Los Angeles) = %+v, want %+v", got, want)
	}
	if got, want := HotWindows(now, tokyo), []DateRange{
		{StartDate: "2026-04-01", EndDate: "2026-04-01", Days: 1},
		{StartDate: "2026-03-31", EndDate: "2026-03-31", Days: 1},
		{StartDate: "2026-03-26", EndDate: "2026-04-01", Days: 7},
		{StartDate: "2026-03-02", EndDate: "2026-04-01", Days: 31},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HotWindows(Tokyo) = %+v, want %+v", got, want)
	}
}

func TestPublicationGoldenFencesStaleWorkersAndSkipsIdempotentWindow(t *testing.T) {
	state := PublicationState{SourceWatermark: "2026-07-22T11:59:00Z", RefreshDate: "2026-07-22", Fence: 9}

	if got := DecidePublication(state, PublicationState{SourceWatermark: "2026-07-22T12:00:00Z", RefreshDate: "2026-07-22", Fence: 9}); got.Kind != RejectStaleFence {
		t.Fatalf("same fence decision = %+v, want stale rejection", got)
	}
	if got := DecidePublication(state, PublicationState{SourceWatermark: "2026-07-22T11:58:59Z", RefreshDate: "2026-07-22", Fence: 10}); got.Kind != RejectStaleWatermark {
		t.Fatalf("older watermark decision = %+v, want stale watermark rejection", got)
	}
	if got := DecidePublication(state, PublicationState{SourceWatermark: state.SourceWatermark, RefreshDate: state.RefreshDate, Fence: 10}); got != (PublicationDecision{Kind: SkipUnchanged, Checkpoint: PublicationState{SourceWatermark: state.SourceWatermark, RefreshDate: state.RefreshDate, Fence: 10}}) {
		t.Fatalf("idempotent decision = %+v, want checkpoint-only skip", got)
	}
	if got := DecidePublication(state, PublicationState{SourceWatermark: "2026-07-22T12:00:00Z", RefreshDate: "2026-07-22", Fence: 10}); got != (PublicationDecision{Kind: Publish, Checkpoint: PublicationState{SourceWatermark: "2026-07-22T12:00:00Z", RefreshDate: "2026-07-22", Fence: 10}}) {
		t.Fatalf("new watermark decision = %+v, want publish", got)
	}
}

func TestLatestAccessSnapshotGoldenNeverMovesBackward(t *testing.T) {
	snapshots := []AccessSnapshot{
		{AccountID: "account-a", LastUsedAt: at("2026-07-22T10:00:00Z"), Cursor: Cursor{CreatedAt: at("2026-07-22T10:00:00Z"), ID: "usage-001"}},
		{AccountID: "account-a", LastUsedAt: at("2026-07-22T09:00:00Z"), Cursor: Cursor{CreatedAt: at("2026-07-22T11:00:00Z"), ID: "usage-002"}},
		{AccountID: "account-a", LastUsedAt: at("2026-07-22T10:00:00Z"), Cursor: Cursor{CreatedAt: at("2026-07-22T10:01:00Z"), ID: "usage-003"}},
		{AccountID: "account-b", LastUsedAt: at("2026-07-22T11:00:00Z"), Cursor: Cursor{CreatedAt: at("2026-07-22T11:00:00Z"), ID: "usage-004"}},
	}

	got := LatestAccessSnapshots(snapshots)
	if got["account-a"].LastUsedAt != at("2026-07-22T10:00:00Z") || got["account-a"].Cursor.ID != "usage-003" {
		t.Fatalf("account-a latest snapshot = %+v, want latest time with deterministic cursor", got["account-a"])
	}
	if got["account-b"].LastUsedAt != at("2026-07-22T11:00:00Z") {
		t.Fatalf("account-b latest snapshot = %+v", got["account-b"])
	}
}

func at(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
