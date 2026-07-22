package managementusagerecords

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	usageRecordFirstPageResponseSize = 20
	usageRecordFirstPageStoredLimit  = 50
	usageRecordFirstPageCacheTTL     = 36 * time.Hour
	usageRecordFirstPageLeaseTTL     = 2 * time.Second
	usageRecordFirstPageLeaseTries   = 3
)

var errUsageRecordFirstPageCacheMiss = errors.New("usage record first page cache miss")

// FirstPageCache is deliberately a read-side cache port. Cache failures must
// never alter usage-record persistence or acknowledgement behavior.
type FirstPageCache interface {
	Get(context.Context, string) ([]byte, error)
	AcquireLease(context.Context, string, string, time.Duration) (bool, error)
	SetIfLeaseOwner(context.Context, string, string, []byte, time.Duration) (bool, error)
	ReleaseLease(context.Context, string, string) error
}

type usageRecordFirstPageCacheEntry struct {
	Items   []Summary `json:"items"`
	Total   int       `json:"total"`
	HasMore bool      `json:"hasMore"`
}

type usageRecordFirstPageEligibility struct {
	cacheKey string
}

func (s *Service) firstPageEligibility(input ListInput, page, pageSize int, startAt, endAt time.Time, location *time.Location) (usageRecordFirstPageEligibility, bool) {
	if s.firstPageCache == nil || strings.TrimSpace(input.ScopeSystemAccountID) == "" || input.IncludeSystemAccount {
		return usageRecordFirstPageEligibility{}, false
	}
	if page != 1 || !input.PageSizeProvided || input.PageSize != usageRecordFirstPageResponseSize || pageSize != usageRecordFirstPageResponseSize {
		return usageRecordFirstPageEligibility{}, false
	}
	if strings.TrimSpace(input.SortOrder) != "" && strings.TrimSpace(input.SortOrder) != "desc" {
		return usageRecordFirstPageEligibility{}, false
	}
	if normalizedTrafficSource(input.TrafficSource) != "gateway" || strings.TrimSpace(input.TraceID) != "" || strings.TrimSpace(input.AccountKeyword) != "" || strings.TrimSpace(input.ClientIP) != "" || strings.TrimSpace(input.GroupID) != "" || strings.TrimSpace(input.Model) != "" || strings.TrimSpace(input.Result) != "" || normalizedStatusCode(input.StatusCode) != nil {
		return usageRecordFirstPageEligibility{}, false
	}
	if location == nil {
		return usageRecordFirstPageEligibility{}, false
	}
	now := s.now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	if !startAt.Equal(today.UTC()) || !endAt.Equal(today.AddDate(0, 0, 1).UTC()) {
		return usageRecordFirstPageEligibility{}, false
	}
	return usageRecordFirstPageEligibility{cacheKey: usageRecordFirstPageCacheKey(strings.TrimSpace(input.ScopeSystemAccountID), today.Format(time.DateOnly))}, true
}

func usageRecordFirstPageCacheKey(systemAccountID, date string) string {
	return "usage-record-first-page:v1:" + systemAccountID + ":" + date
}

func (s *Service) getFirstPage(ctx context.Context, eligibility usageRecordFirstPageEligibility) (ListResult, bool) {
	raw, err := s.firstPageCache.Get(ctx, eligibility.cacheKey)
	if err != nil {
		return ListResult{}, false
	}
	var entry usageRecordFirstPageCacheEntry
	if json.Unmarshal(raw, &entry) != nil || !validUsageRecordFirstPageEntry(entry) {
		return ListResult{}, false
	}
	entry = mergeUsageRecordFirstPageEntries(entry, usageRecordFirstPageCacheEntry{})
	items := append([]Summary(nil), entry.Items...)
	for index := range items {
		items[index].SystemAccountID = ""
		items[index].SystemAccountName = ""
	}
	if len(items) > usageRecordFirstPageResponseSize {
		items = items[:usageRecordFirstPageResponseSize]
	}
	total := max(entry.Total, usageRecordFirstPageTotalLowerBound(len(entry.Items), entry.HasMore))
	return ListResult{Items: items, Total: total, HasMore: entry.HasMore, Page: 1, PageSize: usageRecordFirstPageResponseSize}, true
}

func (s *Service) seedFirstPage(ctx context.Context, eligibility usageRecordFirstPageEligibility, result ListResult) {
	if len(result.Items) > usageRecordFirstPageResponseSize || result.Page != 1 || result.PageSize != usageRecordFirstPageResponseSize {
		return
	}
	fresh := usageRecordFirstPageCacheEntry{
		Items: append([]Summary(nil), result.Items...), Total: result.Total, HasMore: result.HasMore,
	}
	for attempt := 0; attempt < usageRecordFirstPageLeaseTries; attempt++ {
		token := uuid.NewString()
		acquired, err := s.firstPageCache.AcquireLease(ctx, eligibility.cacheKey, token, usageRecordFirstPageLeaseTTL)
		if err != nil {
			return
		}
		if !acquired {
			if attempt+1 < usageRecordFirstPageLeaseTries {
				s.sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
			}
			continue
		}
		func() {
			defer func() { _ = s.firstPageCache.ReleaseLease(ctx, eligibility.cacheKey, token) }()
			existing := usageRecordFirstPageCacheEntry{}
			if raw, err := s.firstPageCache.Get(ctx, eligibility.cacheKey); err == nil {
				var decoded usageRecordFirstPageCacheEntry
				if json.Unmarshal(raw, &decoded) == nil && validUsageRecordFirstPageEntry(decoded) {
					existing = decoded
				}
			}
			merged := mergeUsageRecordFirstPageEntries(fresh, existing)
			encoded, err := json.Marshal(merged)
			if err != nil {
				return
			}
			_, _ = s.firstPageCache.SetIfLeaseOwner(ctx, eligibility.cacheKey, token, encoded, usageRecordFirstPageCacheTTL)
		}()
		return
	}
}

func mergeUsageRecordFirstPageEntries(fresh, existing usageRecordFirstPageCacheEntry) usageRecordFirstPageCacheEntry {
	byID := make(map[string]Summary, len(fresh.Items)+len(existing.Items))
	for _, item := range append(append([]Summary(nil), fresh.Items...), existing.Items...) {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.CreatedAt) == "" {
			continue
		}
		if _, found := byID[item.ID]; !found {
			byID[item.ID] = item
		}
	}
	items := make([]Summary, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt == items[right].CreatedAt {
			return items[left].ID > items[right].ID
		}
		return items[left].CreatedAt > items[right].CreatedAt
	})
	if len(items) > usageRecordFirstPageStoredLimit {
		items = items[:usageRecordFirstPageStoredLimit]
	}
	hasMore := fresh.HasMore || existing.HasMore || len(items) > usageRecordFirstPageResponseSize
	total := max(fresh.Total, existing.Total, usageRecordFirstPageTotalLowerBound(len(items), hasMore))
	return usageRecordFirstPageCacheEntry{Items: items, Total: total, HasMore: hasMore}
}

func validUsageRecordFirstPageEntry(entry usageRecordFirstPageCacheEntry) bool {
	if len(entry.Items) > usageRecordFirstPageStoredLimit || entry.Total < 0 {
		return false
	}
	if entry.HasMore {
		if len(entry.Items) < usageRecordFirstPageResponseSize || entry.Total < usageRecordFirstPageTotalLowerBound(len(entry.Items), true) {
			return false
		}
	} else if len(entry.Items) > usageRecordFirstPageResponseSize || entry.Total != len(entry.Items) {
		return false
	}
	seen := make(map[string]struct{}, len(entry.Items))
	for _, item := range entry.Items {
		if strings.TrimSpace(item.ID) == "" || item.TrafficSource != "gateway" {
			return false
		}
		if _, found := seen[item.ID]; found {
			return false
		}
		seen[item.ID] = struct{}{}
		if _, err := time.Parse(jsTimeLayout, item.CreatedAt); err != nil {
			return false
		}
	}
	return true
}

func usageRecordFirstPageTotalLowerBound(itemCount int, hasMore bool) int {
	if itemCount > usageRecordFirstPageResponseSize {
		return itemCount
	}
	return itemCount + boolInt(hasMore)
}
