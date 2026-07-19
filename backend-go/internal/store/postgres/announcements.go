package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

const (
	defaultAnnouncementPageSize = 50
	maxAnnouncementPageSize     = 100
	maxPublicAnnouncementLimit  = 30
	announcementListWindowRows  = 1001
)

func (s *Store) ListPublicAnnouncements(ctx context.Context, systemAccountID string, limit int) ([]port.Announcement, error) {
	limit = normalizeAnnouncementLimit(limit)
	rows, err := s.queries().ListPublicAnnouncements(ctx, postgresqueries.ListPublicAnnouncementsParams{
		SystemAccountID: systemAccountID,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list public announcements: %w", err)
	}
	items := make([]port.Announcement, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicAnnouncementFromRow(row))
	}
	return items, nil
}

func (s *Store) MarkVisibleAnnouncementsRead(ctx context.Context, systemAccountID string, announcementIDs []string, readAt time.Time) (int, error) {
	ids := uniqueAnnouncementIDs(announcementIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := s.queries().MarkVisibleAnnouncementsRead(ctx, postgresqueries.MarkVisibleAnnouncementsReadParams{
		AnnouncementIds: ids,
		SystemAccountID: systemAccountID,
		ReadAt:          pgTimestamptz(readAt),
	})
	if err != nil {
		return 0, fmt.Errorf("mark visible announcements read: %w", err)
	}
	return len(rows), nil
}

func (s *Store) ListManagementAnnouncements(ctx context.Context, page int, pageSize int) (port.AnnouncementPage, error) {
	page, pageSize = normalizeAnnouncementPage(page, pageSize)
	rows, err := s.queries().ListManagementAnnouncements(ctx, postgresqueries.ListManagementAnnouncementsParams{
		RowOffset: int32((page - 1) * pageSize),
		RowLimit:  int32(pageSize + 1),
	})
	if err != nil {
		return port.AnnouncementPage{}, fmt.Errorf("list management announcements: %w", err)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items := make([]port.Announcement, 0, len(rows))
	for _, row := range rows {
		items = append(items, managementAnnouncementFromListRow(row))
	}
	return port.AnnouncementPage{
		Items:          items,
		Page:           page,
		PageSize:       pageSize,
		PageUpperBound: (page-1)*pageSize + len(items) + boolInt(hasMore),
		HasMore:        hasMore,
	}, nil
}

func (s *Store) FindManagementAnnouncement(ctx context.Context, id string) (port.Announcement, bool, error) {
	row, err := s.queries().FindManagementAnnouncement(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.Announcement{}, false, nil
	}
	if err != nil {
		return port.Announcement{}, false, fmt.Errorf("find management announcement: %w", err)
	}
	return managementAnnouncementFromDetailRow(row), true, nil
}

func (s *Store) AnnouncementInTx(ctx context.Context, fn func(context.Context, port.AnnouncementTxStore) error) error {
	return announcementInTx(ctx, s.InTx, fn)
}

func announcementInTx(ctx context.Context, inTx func(context.Context, TxFunc) error, fn func(context.Context, port.AnnouncementTxStore) error) error {
	return inTx(ctx, func(ctx context.Context, q Reader) error {
		txQueries, ok := q.(*postgresqueries.Queries)
		if !ok {
			return fmt.Errorf("announcement transaction query type is invalid")
		}
		return fn(ctx, announcementTxStore{queries: txQueries})
	})
}

type announcementTxStore struct {
	queries *postgresqueries.Queries
}

func (s announcementTxStore) FindAnnouncementForUpdate(ctx context.Context, id string) (port.Announcement, bool, error) {
	row, err := s.queries.FindAnnouncementForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.Announcement{}, false, nil
	}
	if err != nil {
		return port.Announcement{}, false, fmt.Errorf("find announcement for update: %w", err)
	}
	return announcementFromModel(row), true, nil
}

func (s announcementTxStore) CreateAnnouncement(ctx context.Context, input port.AnnouncementCreateInput) (port.Announcement, error) {
	row, err := s.queries.CreateAnnouncement(ctx, postgresqueries.CreateAnnouncementParams{
		ID:          input.ID,
		Title:       input.Title,
		Content:     input.Content,
		Level:       input.Level,
		Status:      input.Status,
		ActorID:     input.ActorID,
		PublishedAt: announcementPgTimestamptzPtr(input.PublishedAt),
		NowAt:       pgTimestamptz(input.Now),
	})
	if err != nil {
		return port.Announcement{}, fmt.Errorf("create announcement: %w", err)
	}
	return announcementFromModel(row), nil
}

func (s announcementTxStore) UpdateAnnouncement(ctx context.Context, input port.AnnouncementUpdateInput) (port.Announcement, bool, error) {
	row, err := s.queries.UpdateAnnouncement(ctx, postgresqueries.UpdateAnnouncementParams{
		ID:          input.ID,
		Title:       input.Title,
		Content:     input.Content,
		Level:       input.Level,
		Status:      input.Status,
		ActorID:     input.ActorID,
		PublishedAt: announcementPgTimestamptzPtr(input.PublishedAt),
		NowAt:       pgTimestamptz(input.Now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.Announcement{}, false, nil
	}
	if err != nil {
		return port.Announcement{}, false, fmt.Errorf("update announcement: %w", err)
	}
	return announcementFromModel(row), true, nil
}

func (s announcementTxStore) PublishAnnouncement(ctx context.Context, id string, actorID string, now time.Time) (port.Announcement, bool, error) {
	row, err := s.queries.PublishAnnouncement(ctx, postgresqueries.PublishAnnouncementParams{
		ID: id, ActorID: actorID, NowAt: pgTimestamptz(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.Announcement{}, false, nil
	}
	if err != nil {
		return port.Announcement{}, false, fmt.Errorf("publish announcement: %w", err)
	}
	return announcementFromModel(row), true, nil
}

func (s announcementTxStore) ArchiveAnnouncement(ctx context.Context, id string, actorID string, now time.Time) (port.Announcement, bool, error) {
	row, err := s.queries.ArchiveAnnouncement(ctx, postgresqueries.ArchiveAnnouncementParams{
		ID: id, ActorID: actorID, NowAt: pgTimestamptz(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return port.Announcement{}, false, nil
	}
	if err != nil {
		return port.Announcement{}, false, fmt.Errorf("archive announcement: %w", err)
	}
	return announcementFromModel(row), true, nil
}

func (s announcementTxStore) DeleteAnnouncement(ctx context.Context, id string) (bool, error) {
	rows, err := s.queries.DeleteAnnouncement(ctx, id)
	if err != nil {
		return false, fmt.Errorf("delete announcement: %w", err)
	}
	return rows == 1, nil
}

func (s announcementTxStore) DeleteAnnouncementReads(ctx context.Context, id string) (int, error) {
	rows, err := s.queries.DeleteAnnouncementReads(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("delete announcement reads: %w", err)
	}
	return int(rows), nil
}

func publicAnnouncementFromRow(row postgresqueries.ListPublicAnnouncementsRow) port.Announcement {
	return port.Announcement{
		ID: row.ID, Title: row.Title, Content: row.Content, Level: row.Level, Status: row.Status,
		PublishedAt: timestamptzPtr(row.PublishedAt), ReadAt: timestamptzPtr(row.ReadAt),
		CreatedAt: timestamptzValue(row.CreatedAt), UpdatedAt: timestamptzValue(row.UpdatedAt),
	}
}

func managementAnnouncementFromListRow(row postgresqueries.ListManagementAnnouncementsRow) port.Announcement {
	return port.Announcement{
		ID: row.ID, Title: row.Title, Content: row.Content, Level: row.Level, Status: row.Status,
		CreatedBy: row.CreatedBy, CreatedByName: textValue(row.CreatedByName),
		UpdatedBy: pgTextPtrValue(row.UpdatedBy), UpdatedByName: pgTextPtrValue(row.UpdatedByName),
		PublishedAt: timestamptzPtr(row.PublishedAt), CreatedAt: timestamptzValue(row.CreatedAt), UpdatedAt: timestamptzValue(row.UpdatedAt),
	}
}

func managementAnnouncementFromDetailRow(row postgresqueries.FindManagementAnnouncementRow) port.Announcement {
	return port.Announcement{
		ID: row.ID, Title: row.Title, Content: row.Content, Level: row.Level, Status: row.Status,
		CreatedBy: row.CreatedBy, CreatedByName: textValue(row.CreatedByName),
		UpdatedBy: pgTextPtrValue(row.UpdatedBy), UpdatedByName: pgTextPtrValue(row.UpdatedByName),
		PublishedAt: timestamptzPtr(row.PublishedAt), CreatedAt: timestamptzValue(row.CreatedAt), UpdatedAt: timestamptzValue(row.UpdatedAt),
	}
}

func announcementFromModel(row postgresqueries.JuheBusinessAnnouncement) port.Announcement {
	return port.Announcement{
		ID: row.ID, Title: row.Title, Content: row.Content, Level: row.Level, Status: row.Status,
		CreatedBy: row.CreatedBy, UpdatedBy: pgTextPtrValue(row.UpdatedBy),
		PublishedAt: timestamptzPtr(row.PublishedAt), CreatedAt: timestamptzValue(row.CreatedAt), UpdatedAt: timestamptzValue(row.UpdatedAt),
	}
}

func normalizeAnnouncementLimit(limit int) int {
	if limit <= 0 || limit > maxPublicAnnouncementLimit {
		return maxPublicAnnouncementLimit
	}
	return limit
}

func normalizeAnnouncementPage(page int, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultAnnouncementPageSize
	}
	if pageSize > maxAnnouncementPageSize {
		pageSize = maxAnnouncementPageSize
	}
	page = min(page, max(1, (announcementListWindowRows-1)/pageSize))
	return page, pageSize
}

func uniqueAnnouncementIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, min(len(ids), maxPublicAnnouncementLimit))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
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

func announcementPgTimestamptzPtr(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgTimestamptz(*value)
}

func pgTextPtrValue(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

var _ port.AnnouncementStore = (*Store)(nil)
var _ port.AnnouncementTxStore = announcementTxStore{}
