package port

import (
	"context"
	"time"
)

type Announcement struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Content       string     `json:"content"`
	Level         string     `json:"level"`
	Status        string     `json:"status"`
	CreatedBy     string     `json:"createdBy,omitempty"`
	CreatedByName string     `json:"createdByName,omitempty"`
	UpdatedBy     *string    `json:"updatedBy,omitempty"`
	UpdatedByName *string    `json:"updatedByName,omitempty"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
	ReadAt        *time.Time `json:"readAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type AnnouncementPage struct {
	Items          []Announcement `json:"items"`
	Page           int            `json:"page"`
	PageSize       int            `json:"pageSize"`
	PageUpperBound int            `json:"total"`
	HasMore        bool           `json:"hasMore"`
}

type AnnouncementCreateInput struct {
	ID          string
	Title       string
	Content     string
	Level       string
	Status      string
	ActorID     string
	PublishedAt *time.Time
	Now         time.Time
}

type AnnouncementUpdateInput struct {
	ID          string
	Title       string
	Content     string
	Level       string
	Status      string
	ActorID     string
	PublishedAt *time.Time
	Now         time.Time
}

type AnnouncementStore interface {
	ListPublicAnnouncements(ctx context.Context, systemAccountID string, limit int) ([]Announcement, error)
	MarkVisibleAnnouncementsRead(ctx context.Context, systemAccountID string, announcementIDs []string, readAt time.Time) (int, error)
	ListManagementAnnouncements(ctx context.Context, page int, pageSize int) (AnnouncementPage, error)
	FindManagementAnnouncement(ctx context.Context, id string) (Announcement, bool, error)
	AnnouncementInTx(ctx context.Context, fn func(context.Context, AnnouncementTxStore) error) error
}

type AnnouncementTxStore interface {
	FindAnnouncementForUpdate(ctx context.Context, id string) (Announcement, bool, error)
	CreateAnnouncement(ctx context.Context, input AnnouncementCreateInput) (Announcement, error)
	UpdateAnnouncement(ctx context.Context, input AnnouncementUpdateInput) (Announcement, bool, error)
	PublishAnnouncement(ctx context.Context, id string, actorID string, now time.Time) (Announcement, bool, error)
	ArchiveAnnouncement(ctx context.Context, id string, actorID string, now time.Time) (Announcement, bool, error)
	DeleteAnnouncement(ctx context.Context, id string) (bool, error)
	DeleteAnnouncementReads(ctx context.Context, id string) (int, error)
}
