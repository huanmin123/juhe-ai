package managementauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrProfileNotFound              = errors.New("management auth profile not found")
	ErrProfileDisplayNameExists     = errors.New("management auth profile display name exists")
	ErrProfileDisplayNameInvalid    = errors.New("management auth profile display name invalid")
	ErrProfileDisplayNameWhitespace = errors.New("management auth profile display name whitespace")
)

type CurrentUserProfile struct {
	ID                 string
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
}

type ProfileUpdateInput struct {
	AuthContext Context
	DisplayName string
}

type ProfileUpdateResult struct {
	Before  CurrentUserProfile
	Account CurrentUserProfile
	Changed bool
}

type ProfileService struct {
	store port.ManagementCurrentUserProfileWriter
	now   func() time.Time
}

type ProfileServiceOptions struct {
	Store port.ManagementCurrentUserProfileWriter
	Now   func() time.Time
}

func NewProfileService(store port.ManagementCurrentUserProfileWriter) *ProfileService {
	return NewProfileServiceWithOptions(ProfileServiceOptions{Store: store})
}

func NewProfileServiceWithOptions(opts ProfileServiceOptions) *ProfileService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &ProfileService{store: opts.Store, now: now}
}

func NormalizeProfileDisplayName(value string) (string, error) {
	if value == "" {
		return "", ErrProfileDisplayNameInvalid
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return "", ErrProfileDisplayNameWhitespace
	}
	displayName := strings.TrimSpace(value)
	if displayName == "" {
		return "", ErrProfileDisplayNameInvalid
	}
	return displayName, nil
}

func (s *ProfileService) UpdateProfile(ctx context.Context, input ProfileUpdateInput) (ProfileUpdateResult, error) {
	if s.store == nil {
		return ProfileUpdateResult{}, fmt.Errorf("management auth profile store is required")
	}
	displayName, err := NormalizeProfileDisplayName(input.DisplayName)
	if err != nil {
		return ProfileUpdateResult{}, err
	}
	authContext := input.AuthContext
	systemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	if systemAccountID == "" {
		return ProfileUpdateResult{}, ErrProfileNotFound
	}
	before := currentUserProfileFromAuthContext(authContext)
	if before.DisplayName == displayName {
		return ProfileUpdateResult{
			Before:  before,
			Account: before,
			Changed: false,
		}, nil
	}
	updated, found, err := s.store.UpdateManagementCurrentUserProfile(ctx, port.ManagementCurrentUserProfileUpdateInput{
		SystemAccountID: systemAccountID,
		DisplayName:     displayName,
		UpdatedAt:       s.now().UTC(),
	})
	if errors.Is(err, port.ErrManagementProfileDisplayNameExists) {
		return ProfileUpdateResult{}, ErrProfileDisplayNameExists
	}
	if err != nil {
		return ProfileUpdateResult{}, err
	}
	if !found {
		return ProfileUpdateResult{}, ErrProfileNotFound
	}
	result := ProfileUpdateResult{
		Before:  currentUserProfileFromPort(updated.Before),
		Account: currentUserProfileFromPort(updated.Account),
	}
	result.Changed = result.Before.DisplayName != result.Account.DisplayName
	return result, nil
}

func currentUserProfileFromAuthContext(authContext Context) CurrentUserProfile {
	return CurrentUserProfile{
		ID:                 authContext.SystemAccountID,
		Username:           authContext.Username,
		DisplayName:        authContext.DisplayName,
		Role:               authContext.Role,
		MustChangePassword: authContext.MustChangePassword,
	}
}

func currentUserProfileFromPort(profile port.ManagementCurrentUserProfile) CurrentUserProfile {
	return CurrentUserProfile{
		ID:                 profile.ID,
		Username:           profile.Username,
		DisplayName:        profile.DisplayName,
		Role:               profile.Role,
		MustChangePassword: profile.MustChangePassword && !IsAdminRole(profile.Role),
	}
}
