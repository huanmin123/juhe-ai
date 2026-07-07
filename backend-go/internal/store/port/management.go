package port

import (
	"context"
	"time"
)

type ManagementSessionAccount struct {
	SessionID          string
	TokenHash          string
	ExpiresAt          time.Time
	LastSeenAt         time.Time
	AccountID          string
	Username           string
	DisplayName        string
	Role               string
	Status             string
	MustChangePassword bool
}

type ManagementSessionReader interface {
	FindManagementSessionByTokenHash(ctx context.Context, tokenHash string) (ManagementSessionAccount, bool, error)
}

type ManagementProxyOption struct {
	ID      string
	Name    string
	Type    string
	Enabled bool
}

type ManagementProxyOptionListInput struct {
	Keyword string
	Limit   int
}

type ManagementProxyOptionReader interface {
	ListManagementProxyOptions(ctx context.Context, input ManagementProxyOptionListInput) ([]ManagementProxyOption, error)
}
