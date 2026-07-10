package managementsettings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrUpdateEmpty  = errors.New("全局设置更新不能为空")
	ErrAppNameEmpty = errors.New("appName 必须是非空字符串")
	ErrAppIconEmpty = errors.New("appIcon 必须是非空字符串")
)

type Store interface {
	port.PublicSettingsReader
	port.ManagementGlobalSettingsWriter
}

type GlobalSettingsCacheInvalidator interface {
	InvalidateGlobalSettingsCache(ctx context.Context) error
}

type Service struct {
	store       Store
	invalidator GlobalSettingsCacheInvalidator
	now         func() time.Time
}

type ServiceOptions struct {
	Store                          Store
	GlobalSettingsCacheInvalidator GlobalSettingsCacheInvalidator
	Now                            func() time.Time
}

type Settings struct {
	AppName string `json:"appName"`
	AppIcon string `json:"appIcon"`
}

type UpdateInput struct {
	AppName *string
	AppIcon *string
}

type UpdateResult struct {
	Before   Settings `json:"before"`
	Settings Settings `json:"settings"`
}

func NewService(store Store) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:       opts.Store,
		invalidator: opts.GlobalSettingsCacheInvalidator,
		now:         now,
	}
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	if s.store == nil {
		return Settings{}, fmt.Errorf("management global settings store is required")
	}
	settings, err := s.store.PublicGlobalSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return settingsFromPort(settings), nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (UpdateResult, error) {
	if s.store == nil {
		return UpdateResult{}, fmt.Errorf("management global settings store is required")
	}
	if input.AppName == nil && input.AppIcon == nil {
		return UpdateResult{}, ErrUpdateEmpty
	}

	appName, err := normalizeRequiredSetting(input.AppName, ErrAppNameEmpty)
	if err != nil {
		return UpdateResult{}, err
	}
	appIcon, err := normalizeRequiredSetting(input.AppIcon, ErrAppIconEmpty)
	if err != nil {
		return UpdateResult{}, err
	}

	result, err := s.store.UpdateGlobalSettings(ctx, port.ManagementGlobalSettingsUpdateInput{
		AppName:   appName,
		AppIcon:   appIcon,
		UpdatedAt: s.now().UTC(),
	})
	if err != nil {
		return UpdateResult{}, err
	}
	s.invalidateGlobalSettingsCache(ctx)
	return UpdateResult{
		Before:   settingsFromPort(result.Before),
		Settings: settingsFromPort(result.Settings),
	}, nil
}

func (s *Service) invalidateGlobalSettingsCache(ctx context.Context) {
	if s.invalidator == nil {
		return
	}
	_ = s.invalidator.InvalidateGlobalSettingsCache(ctx)
}

func normalizeRequiredSetting(value *string, emptyErr error) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, emptyErr
	}
	return &normalized, nil
}

func settingsFromPort(settings port.PublicGlobalSettings) Settings {
	return Settings{
		AppName: settings.AppName,
		AppIcon: settings.AppIcon,
	}
}
