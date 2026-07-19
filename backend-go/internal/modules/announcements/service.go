package announcements

import (
	"context"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const maxPublicAnnouncementLimit = 30

type Service struct {
	store port.AnnouncementStore
	now   func() time.Time
}

type ServiceOptions struct {
	Store port.AnnouncementStore
	Now   func() time.Time
}

type PublicListInput struct {
	SystemAccountID string
	Limit           int
}

type PublicReadInput struct {
	SystemAccountID string
	AnnouncementIDs []string
}

type PublicReadResult struct {
	ReadAt time.Time `json:"readAt"`
	Count  int       `json:"count"`
}

func NewService(store port.AnnouncementStore) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(options ServiceOptions) *Service {
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: options.Store, now: now}
}

func (s *Service) ListPublic(ctx context.Context, input PublicListInput) ([]Announcement, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("announcement store is required")
	}
	items, err := s.store.ListPublicAnnouncements(ctx, input.SystemAccountID, normalizePublicLimit(input.Limit))
	if err != nil {
		return nil, fmt.Errorf("list public announcements: %w", err)
	}
	return items, nil
}

func (s *Service) MarkPublicRead(ctx context.Context, input PublicReadInput) (PublicReadResult, error) {
	if s == nil || s.store == nil {
		return PublicReadResult{}, fmt.Errorf("announcement store is required")
	}
	readAt := s.now().UTC()
	ids := normalizeAnnouncementIDs(input.AnnouncementIDs)
	if len(ids) == 0 {
		return PublicReadResult{ReadAt: readAt}, nil
	}
	count, err := s.store.MarkVisibleAnnouncementsRead(ctx, input.SystemAccountID, ids, readAt)
	if err != nil {
		return PublicReadResult{}, fmt.Errorf("mark public announcements read: %w", err)
	}
	return PublicReadResult{ReadAt: readAt, Count: count}, nil
}

func normalizePublicLimit(limit int) int {
	if limit <= 0 {
		return maxPublicAnnouncementLimit
	}
	if limit > maxPublicAnnouncementLimit {
		return maxPublicAnnouncementLimit
	}
	return limit
}

func normalizeAnnouncementIDs(ids []string) []string {
	result := make([]string, 0, min(len(ids), maxPublicAnnouncementLimit))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
		if len(result) == maxPublicAnnouncementLimit {
			break
		}
	}
	return result
}
