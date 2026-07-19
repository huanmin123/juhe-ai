package announcements

import "juhe-ai/backend-go/internal/store/port"

type Announcement = port.Announcement
type Page = port.AnnouncementPage

const (
	LevelCritical = "critical"
	LevelWarning  = "warning"
	LevelInfo     = "info"
	LevelNormal   = "normal"

	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusArchived  = "archived"
)
