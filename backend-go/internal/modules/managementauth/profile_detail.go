package managementauth

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

type ProfileDetailService struct {
	reader port.ManagementCurrentUserProfileReader
}

func NewProfileDetailService(reader port.ManagementCurrentUserProfileReader) *ProfileDetailService {
	return &ProfileDetailService{reader: reader}
}

func (s *ProfileDetailService) GetProfile(ctx context.Context, authContext Context) (SystemAccountSummary, error) {
	if s.reader == nil {
		return SystemAccountSummary{}, fmt.Errorf("management auth profile reader is required")
	}
	systemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	if systemAccountID == "" {
		return SystemAccountSummary{}, ErrProfileNotFound
	}
	account, found, err := s.reader.FindManagementCurrentUserProfile(ctx, systemAccountID)
	if err != nil {
		return SystemAccountSummary{}, err
	}
	if !found {
		return SystemAccountSummary{}, ErrProfileNotFound
	}
	return systemAccountSummaryFromPort(account), nil
}
