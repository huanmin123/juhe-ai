package usagestatscontract

import (
	"errors"
	"strings"
	"time"
)

const (
	MaxShardsPerBatch = 16
	MaxWindowDays     = 31
)

var (
	ErrCursorNotStrictlyIncreasing = errors.New("usage stats cursor is not strictly increasing")
	ErrCursorBeyondSafetyFence     = errors.New("usage stats cursor is beyond safety fence")
)

type Cursor struct {
	CreatedAt time.Time
	ID        string
}

func CompareCursor(left, right Cursor) int {
	if comparison := left.CreatedAt.Compare(right.CreatedAt); comparison != 0 {
		return comparison
	}
	return strings.Compare(left.ID, right.ID)
}

func IsEligible(row, cursor Cursor, safeCreatedBefore time.Time) bool {
	return CompareCursor(row, cursor) > 0 && !row.CreatedAt.After(safeCreatedBefore)
}

func AdvanceCursor(current Cursor, orderedRows []Cursor, safeCreatedBefore time.Time) (Cursor, error) {
	next := current
	for _, row := range orderedRows {
		if row.CreatedAt.After(safeCreatedBefore) {
			return current, ErrCursorBeyondSafetyFence
		}
		if CompareCursor(row, next) <= 0 {
			return current, ErrCursorNotStrictlyIncreasing
		}
		next = row
	}
	return next, nil
}

type ShardWindow struct {
	Indexes    []int
	NextOffset int
}

func PlanShardWindow(total, offset, batchLimit int) ShardWindow {
	if total <= 0 || batchLimit <= 0 {
		return ShardWindow{}
	}
	windowSize := min(total, MaxShardsPerBatch, batchLimit)
	normalizedOffset := offset % total
	if normalizedOffset < 0 {
		normalizedOffset += total
	}
	indexes := make([]int, windowSize)
	for index := range indexes {
		indexes[index] = (normalizedOffset + index) % total
	}
	return ShardWindow{
		Indexes:    indexes,
		NextOffset: (normalizedOffset + windowSize) % total,
	}
}

type DateRange struct {
	StartDate string
	EndDate   string
	Days      int
}

func HotWindows(now time.Time, location *time.Location) []DateRange {
	if location == nil {
		location = time.UTC
	}
	year, month, day := now.In(location).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	fixedStart := today.AddDate(0, 0, -(MaxWindowDays - 1))
	monthStart := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	if monthStart.Before(fixedStart) {
		monthStart = fixedStart
	}
	candidates := [][2]time.Time{
		{today, today},
		{today.AddDate(0, 0, -1), today.AddDate(0, 0, -1)},
		{today.AddDate(0, 0, -6), today},
		{fixedStart, today},
		{monthStart, today},
	}

	result := make([]DateRange, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		startDate := candidate[0].Format(time.DateOnly)
		endDate := candidate[1].Format(time.DateOnly)
		key := startDate + ":" + endDate
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, DateRange{
			StartDate: startDate,
			EndDate:   endDate,
			Days:      int(candidate[1].Sub(candidate[0])/(24*time.Hour)) + 1,
		})
	}
	return result
}

type PublicationState struct {
	SourceWatermark string
	RefreshDate     string
	Fence           uint64
}

type PublicationKind uint8

const (
	RejectStaleFence PublicationKind = iota
	RejectStaleWatermark
	SkipUnchanged
	Publish
)

type PublicationDecision struct {
	Kind       PublicationKind
	Checkpoint PublicationState
}

func DecidePublication(current, candidate PublicationState) PublicationDecision {
	if candidate.Fence <= current.Fence {
		return PublicationDecision{Kind: RejectStaleFence, Checkpoint: current}
	}
	if current.SourceWatermark != "" && candidate.SourceWatermark != "" && strings.Compare(candidate.SourceWatermark, current.SourceWatermark) < 0 {
		return PublicationDecision{Kind: RejectStaleWatermark, Checkpoint: current}
	}
	decision := PublicationDecision{Kind: Publish, Checkpoint: candidate}
	if candidate.SourceWatermark == current.SourceWatermark && candidate.RefreshDate == current.RefreshDate {
		decision.Kind = SkipUnchanged
	}
	return decision
}

type AccessSnapshot struct {
	AccountID  string
	LastUsedAt time.Time
	Cursor     Cursor
}

func LatestAccessSnapshots(snapshots []AccessSnapshot) map[string]AccessSnapshot {
	latest := make(map[string]AccessSnapshot)
	for _, candidate := range snapshots {
		candidate.AccountID = strings.TrimSpace(candidate.AccountID)
		if candidate.AccountID == "" || candidate.LastUsedAt.IsZero() {
			continue
		}
		current, exists := latest[candidate.AccountID]
		if !exists || candidate.LastUsedAt.After(current.LastUsedAt) ||
			(candidate.LastUsedAt.Equal(current.LastUsedAt) && CompareCursor(candidate.Cursor, current.Cursor) > 0) {
			latest[candidate.AccountID] = candidate
		}
	}
	return latest
}
