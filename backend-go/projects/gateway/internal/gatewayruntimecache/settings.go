package gatewayruntimecache

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// gateway settings cache (Node readCachedGatewaySettings /
// readCachedGatewaySettingsAsync)
// ---------------------------------------------------------------------------

// ReadCachedGatewaySettings mirrors readCachedGatewaySettings: cache-first
// single-slot read, one loader call per TTL window.
func (s *Service) ReadCachedGatewaySettings(ctx context.Context) (GatewaySettings, error) {
	if cached, ok := s.settingsCache.get("current"); ok {
		return CloneGatewaySettings(cached), nil
	}
	value, err := s.models.ReadGatewaySettings(ctx)
	if err != nil {
		return GatewaySettings{}, err
	}
	s.settingsCache.set("current", value, gatewaySettingsTTL)
	return CloneGatewaySettings(value), nil
}

// ReadCachedGatewaySettingsAsync mirrors readCachedGatewaySettingsAsync: the
// invalidation pre-sync runs first; without a shared cache layer the local
// entry is the fact source, with one the shared entry wins over a cold local.
func (s *Service) ReadCachedGatewaySettingsAsync(ctx context.Context) (GatewaySettings, error) {
	s.syncInvalidationsBestEffort(ctx)
	if s.sharedSettings == nil {
		return s.ReadCachedGatewaySettings(ctx)
	}
	if cached, ok := s.settingsCache.get("current"); ok {
		return CloneGatewaySettings(cached), nil
	}
	shared, ok, err := s.getSharedSettings(ctx)
	if err != nil {
		s.logSharedFailure("gateway_settings_shared_cache_read_failed", err)
	}
	if ok && shared != nil {
		s.settingsCache.set("current", CloneGatewaySettings(*shared), gatewaySettingsTTL)
		return CloneGatewaySettings(*shared), nil
	}
	value, err := s.models.ReadGatewaySettings(ctx)
	if err != nil {
		return GatewaySettings{}, err
	}
	s.setSettingsCacheEntry(value)
	return CloneGatewaySettings(value), nil
}

func (s *Service) getSharedSettings(ctx context.Context) (*GatewaySettings, bool, error) {
	var value GatewaySettings
	ok, err := s.sharedSettings.Get(ctx, "current", &value)
	if err != nil || !ok {
		return nil, false, err
	}
	clone := CloneGatewaySettings(value)
	return &clone, true, nil
}

// setSettingsCacheEntry mirrors setGatewaySettingsCacheEntryAsync: local write
// always, shared write best-effort.
func (s *Service) setSettingsCacheEntry(settings GatewaySettings) {
	cached := CloneGatewaySettings(settings)
	s.settingsCache.set("current", cached, gatewaySettingsTTL)
	if s.sharedSettings == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.sharedSettings.Set(ctx, "current", cached, gatewaySettingsTTL); err != nil {
		s.logSharedFailure("gateway_settings_shared_cache_write_failed", err)
	}
}

// clearSharedSettings mirrors clearGatewaySettingsSharedCache.
func (s *Service) clearSharedSettings(ctx context.Context) {
	if s.sharedSettings == nil {
		return
	}
	if err := s.sharedSettings.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_settings_shared_cache_clear_failed", err)
	}
}
