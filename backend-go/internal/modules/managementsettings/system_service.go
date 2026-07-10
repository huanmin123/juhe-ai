package managementsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/systemsettings"
)

const SystemSettingsUpdatedReason = "settings_updated"
const systemSettingsInvalidationTimeout = 5 * time.Second

var (
	ErrSystemUpdateEmpty                         = systemsettings.ErrPatchEmpty
	ErrUsageStatsTimezoneOnlineUpdateUnsupported = errors.New(
		"PostgreSQL 模式下暂不支持在线修改统计时区，请停机后通过离线迁移 / 重建流程调整",
	)
)

type SystemStore interface {
	port.ManagementSystemSettingsReader
	port.ManagementSystemSettingsWriter
}

type SystemSettingsInvalidator interface {
	InvalidateSystemSettingsCache(ctx context.Context) error
	InvalidateGatewayRuntime(ctx context.Context, reason string) error
}

type SystemService struct {
	store       SystemStore
	invalidator SystemSettingsInvalidator
	logger      *slog.Logger
	now         func() time.Time
}

type SystemServiceOptions struct {
	Store       SystemStore
	Invalidator SystemSettingsInvalidator
	Logger      *slog.Logger
	Now         func() time.Time
}

type SystemUpdateInput struct {
	Values map[string]json.RawMessage
}

type SystemUpdateResult struct {
	Before   systemsettings.Snapshot `json:"before"`
	Settings systemsettings.Snapshot `json:"settings"`
}

func NewSystemService(store SystemStore) *SystemService {
	return NewSystemServiceWithOptions(SystemServiceOptions{Store: store})
}

func NewSystemServiceWithOptions(opts SystemServiceOptions) *SystemService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SystemService{
		store:       opts.Store,
		invalidator: opts.Invalidator,
		logger:      opts.Logger,
		now:         now,
	}
}

func (s *SystemService) Get(ctx context.Context) (systemsettings.Snapshot, error) {
	if s.store == nil {
		return systemsettings.Snapshot{}, fmt.Errorf("management system settings store is required")
	}
	settings, err := s.store.ManagementSystemSettings(ctx)
	if err != nil {
		return systemsettings.Snapshot{}, err
	}
	validated, err := systemsettings.NewSnapshot(settings.Values())
	if err != nil {
		return systemsettings.Snapshot{}, fmt.Errorf("validate management system settings snapshot: %w", err)
	}
	return validated, nil
}

func (s *SystemService) Update(ctx context.Context, input SystemUpdateInput) (SystemUpdateResult, error) {
	if s.store == nil {
		return SystemUpdateResult{}, fmt.Errorf("management system settings store is required")
	}
	if _, exists := input.Values[systemsettings.UsageStatsTimezoneKey]; exists {
		return SystemUpdateResult{}, ErrUsageStatsTimezoneOnlineUpdateUnsupported
	}
	patch, err := systemsettings.NewPatch(input.Values)
	if err != nil {
		return SystemUpdateResult{}, err
	}

	result, err := s.store.UpdateManagementSystemSettings(ctx, port.ManagementSystemSettingsUpdateInput{
		Patch:     patch,
		UpdatedAt: s.now().UTC(),
	})
	if err != nil {
		return SystemUpdateResult{}, err
	}
	before, err := systemsettings.NewSnapshot(result.Before.Values())
	if err != nil {
		return SystemUpdateResult{}, fmt.Errorf("validate previous management system settings snapshot: %w", err)
	}
	settings, err := systemsettings.NewSnapshot(result.Settings.Values())
	if err != nil {
		return SystemUpdateResult{}, fmt.Errorf("validate updated management system settings snapshot: %w", err)
	}

	s.invalidate(ctx)
	return SystemUpdateResult{
		Before:   before,
		Settings: settings,
	}, nil
}

func (s *SystemService) invalidate(ctx context.Context) {
	if s.invalidator == nil {
		return
	}
	cacheCtx, cancelCache := context.WithTimeout(context.WithoutCancel(ctx), systemSettingsInvalidationTimeout)
	if err := s.invalidator.InvalidateSystemSettingsCache(cacheCtx); err != nil && s.logger != nil {
		s.logger.Warn(
			"系统设置共享缓存失效失败",
			slog.String("event", "system_settings_cache_invalidation_failed"),
			slog.Any("error", err),
		)
	}
	cancelCache()

	runtimeCtx, cancelRuntime := context.WithTimeout(context.WithoutCancel(ctx), systemSettingsInvalidationTimeout)
	if err := s.invalidator.InvalidateGatewayRuntime(runtimeCtx, SystemSettingsUpdatedReason); err != nil && s.logger != nil {
		s.logger.Warn(
			"系统设置网关运行态失效失败",
			slog.String("event", "system_settings_gateway_runtime_invalidation_failed"),
			slog.String("reason", SystemSettingsUpdatedReason),
			slog.Any("error", err),
		)
	}
	cancelRuntime()
}
