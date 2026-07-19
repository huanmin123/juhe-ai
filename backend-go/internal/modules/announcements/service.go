package announcements

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	maxPublicAnnouncementLimit       = 30
	defaultManagementPage            = 1
	defaultManagementPageSize        = 50
	maxManagementPageSize            = 100
	managementAnnouncementWindowRows = 1001
	defaultAnnouncementLevel         = "info"
	defaultAnnouncementStatus        = "draft"
)

var ErrAnnouncementInputInvalid = errors.New("announcement input invalid")
var ErrAnnouncementNotFound = errors.New("announcement not found")

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

type CreateInput struct {
	ID      string
	Title   string
	Content string
	Level   string
	Status  string
	ActorID string
}

type UpdateInput struct {
	ID      string
	Title   *string
	Content *string
	Level   *string
	Status  *string
	ActorID string
}

type ActionInput struct {
	ID      string
	ActorID string
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

func (s *Service) ListManagement(ctx context.Context, page int, pageSize int) (Page, error) {
	if s == nil || s.store == nil {
		return Page{}, fmt.Errorf("announcement store is required")
	}
	pageSize = normalizeManagementPageSize(pageSize)
	page = normalizeManagementPage(page, pageSize)
	result, err := s.store.ListManagementAnnouncements(ctx, page, pageSize)
	if err != nil {
		return Page{}, fmt.Errorf("list management announcements: %w", err)
	}
	return result, nil
}

func (s *Service) FindManagement(ctx context.Context, id string) (Announcement, bool, error) {
	if s == nil || s.store == nil {
		return Announcement{}, false, fmt.Errorf("announcement store is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Announcement{}, false, fmt.Errorf("%w: announcement id is required", ErrAnnouncementInputInvalid)
	}
	result, found, err := s.store.FindManagementAnnouncement(ctx, id)
	if err != nil {
		return Announcement{}, false, fmt.Errorf("find management announcement: %w", err)
	}
	return result, found, nil
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

func (s *Service) Create(ctx context.Context, input CreateInput) (port.Announcement, error) {
	if err := s.requireStore(); err != nil {
		return port.Announcement{}, err
	}
	id, actorID, err := normalizeAnnouncementIdentity(input.ID, input.ActorID)
	if err != nil {
		return port.Announcement{}, err
	}
	title, err := normalizeAnnouncementText(input.Title, "title")
	if err != nil {
		return port.Announcement{}, err
	}
	content, err := normalizeAnnouncementText(input.Content, "content")
	if err != nil {
		return port.Announcement{}, err
	}
	level := input.Level
	if level == "" {
		level = defaultAnnouncementLevel
	}
	if !isAnnouncementLevel(level) {
		return port.Announcement{}, fmt.Errorf("%w: level is invalid", ErrAnnouncementInputInvalid)
	}
	status := input.Status
	if status == "" {
		status = defaultAnnouncementStatus
	}
	if !isAnnouncementStatus(status) {
		return port.Announcement{}, fmt.Errorf("%w: status is invalid", ErrAnnouncementInputInvalid)
	}
	now := s.now().UTC()
	var publishedAt *time.Time
	if status == "published" {
		publishedAt = &now
	}
	var result port.Announcement
	err = s.withAnnouncementTx(ctx, func(txCtx context.Context, tx port.AnnouncementTxStore) error {
		result, err = tx.CreateAnnouncement(txCtx, port.AnnouncementCreateInput{
			ID: id, Title: title, Content: content, Level: level, Status: status,
			ActorID: actorID, PublishedAt: publishedAt, Now: now,
		})
		return err
	})
	if err != nil {
		return port.Announcement{}, fmt.Errorf("create announcement: %w", err)
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (port.Announcement, error) {
	if err := s.requireStore(); err != nil {
		return port.Announcement{}, err
	}
	id, actorID, err := normalizeAnnouncementIdentity(input.ID, input.ActorID)
	if err != nil {
		return port.Announcement{}, err
	}
	var titleInput *string
	if input.Title != nil {
		value, normalizeErr := normalizeAnnouncementText(*input.Title, "title")
		if normalizeErr != nil {
			return port.Announcement{}, normalizeErr
		}
		titleInput = &value
	}
	var contentInput *string
	if input.Content != nil {
		value, normalizeErr := normalizeAnnouncementText(*input.Content, "content")
		if normalizeErr != nil {
			return port.Announcement{}, normalizeErr
		}
		contentInput = &value
	}
	if input.Level != nil && !isAnnouncementLevel(*input.Level) {
		return port.Announcement{}, fmt.Errorf("%w: level is invalid", ErrAnnouncementInputInvalid)
	}
	if input.Status != nil && !isAnnouncementStatus(*input.Status) {
		return port.Announcement{}, fmt.Errorf("%w: status is invalid", ErrAnnouncementInputInvalid)
	}
	var result port.Announcement
	err = s.withAnnouncementTx(ctx, func(txCtx context.Context, tx port.AnnouncementTxStore) error {
		current, found, findErr := tx.FindAnnouncementForUpdate(txCtx, id)
		if findErr != nil {
			return fmt.Errorf("find announcement for update: %w", findErr)
		}
		if !found {
			return ErrAnnouncementNotFound
		}
		title := current.Title
		if titleInput != nil {
			title = *titleInput
		}
		content := current.Content
		if contentInput != nil {
			content = *contentInput
		}
		level := current.Level
		if input.Level != nil {
			level = *input.Level
		}
		status := current.Status
		if input.Status != nil {
			status = *input.Status
		}
		now := s.now().UTC()
		publishedAt := current.PublishedAt
		resetReadState := status == "published" && current.Status != "published"
		if resetReadState {
			publishedAt = &now
		}
		updated, updatedFound, updateErr := tx.UpdateAnnouncement(txCtx, port.AnnouncementUpdateInput{
			ID: id, Title: title, Content: content, Level: level, Status: status,
			ActorID: actorID, PublishedAt: publishedAt, Now: now,
		})
		if updateErr != nil {
			return fmt.Errorf("update announcement: %w", updateErr)
		}
		if !updatedFound {
			return ErrAnnouncementNotFound
		}
		result = updated
		if resetReadState {
			if _, err := tx.DeleteAnnouncementReads(txCtx, id); err != nil {
				return fmt.Errorf("clear announcement reads: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return port.Announcement{}, fmt.Errorf("update announcement: %w", err)
	}
	return result, nil
}

func (s *Service) Publish(ctx context.Context, input ActionInput) (port.Announcement, error) {
	return s.publishOrArchive(ctx, input, true)
}

func (s *Service) Unpublish(ctx context.Context, input ActionInput) (port.Announcement, error) {
	return s.publishOrArchive(ctx, input, false)
}

func (s *Service) publishOrArchive(ctx context.Context, input ActionInput, publish bool) (port.Announcement, error) {
	if err := s.requireStore(); err != nil {
		return port.Announcement{}, err
	}
	id, actorID, err := normalizeAnnouncementIdentity(input.ID, input.ActorID)
	if err != nil {
		return port.Announcement{}, err
	}
	now := s.now().UTC()
	var result port.Announcement
	err = s.withAnnouncementTx(ctx, func(txCtx context.Context, tx port.AnnouncementTxStore) error {
		var found bool
		var err error
		if publish {
			result, found, err = tx.PublishAnnouncement(txCtx, id, actorID, now)
		} else {
			result, found, err = tx.ArchiveAnnouncement(txCtx, id, actorID, now)
		}
		if err != nil {
			return err
		}
		if !found {
			return ErrAnnouncementNotFound
		}
		if publish {
			if _, err := tx.DeleteAnnouncementReads(txCtx, id); err != nil {
				return fmt.Errorf("clear announcement reads: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return port.Announcement{}, fmt.Errorf("change announcement status: %w", err)
	}
	return result, nil
}

func (s *Service) Delete(ctx context.Context, input ActionInput) (port.Announcement, error) {
	if err := s.requireStore(); err != nil {
		return port.Announcement{}, err
	}
	id, _, err := normalizeAnnouncementIdentity(input.ID, input.ActorID)
	if err != nil {
		return port.Announcement{}, err
	}
	var result port.Announcement
	err = s.withAnnouncementTx(ctx, func(txCtx context.Context, tx port.AnnouncementTxStore) error {
		current, found, findErr := tx.FindAnnouncementForUpdate(txCtx, id)
		if findErr != nil {
			return fmt.Errorf("find announcement for delete: %w", findErr)
		}
		if !found {
			return ErrAnnouncementNotFound
		}
		deleted, err := tx.DeleteAnnouncement(txCtx, id)
		if err != nil {
			return fmt.Errorf("delete announcement: %w", err)
		}
		if !deleted {
			return ErrAnnouncementNotFound
		}
		result = current
		return nil
	})
	if err != nil {
		return port.Announcement{}, fmt.Errorf("delete announcement: %w", err)
	}
	return result, nil
}

func (s *Service) withAnnouncementTx(ctx context.Context, fn func(context.Context, port.AnnouncementTxStore) error) error {
	if err := s.requireStore(); err != nil {
		return err
	}
	return s.store.AnnouncementInTx(ctx, fn)
}

func (s *Service) requireStore() error {
	if s == nil || s.store == nil {
		return fmt.Errorf("announcement store is required")
	}
	return nil
}

func normalizeAnnouncementIdentity(id string, actorID string) (string, string, error) {
	id = strings.TrimSpace(id)
	actorID = strings.TrimSpace(actorID)
	if id == "" || actorID == "" {
		return "", "", fmt.Errorf("%w: id and actor are required", ErrAnnouncementInputInvalid)
	}
	return id, actorID, nil
}

func normalizeAnnouncementText(value string, field string) (string, error) {
	value = strings.TrimSpace(value)
	maxLength := 120
	if field == "content" {
		maxLength = 5000
	}
	if announcementUTF16Length(value) < 1 || announcementUTF16Length(value) > maxLength {
		return "", fmt.Errorf("%w: %s length is invalid", ErrAnnouncementInputInvalid, field)
	}
	return value, nil
}

func announcementUTF16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func isAnnouncementLevel(value string) bool {
	switch value {
	case "critical", "warning", "info", "normal":
		return true
	default:
		return false
	}
}

func isAnnouncementStatus(value string) bool {
	switch value {
	case "draft", "published", "archived":
		return true
	default:
		return false
	}
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

func normalizeManagementPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultManagementPageSize
	}
	if pageSize > maxManagementPageSize {
		return maxManagementPageSize
	}
	return pageSize
}

func normalizeManagementPage(page int, pageSize int) int {
	if page < defaultManagementPage {
		page = defaultManagementPage
	}
	maxPage := max(1, (managementAnnouncementWindowRows-1)/max(1, pageSize))
	if page > maxPage {
		return maxPage
	}
	return page
}
