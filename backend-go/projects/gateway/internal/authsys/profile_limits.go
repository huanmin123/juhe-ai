package authsys

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	systemSettingsAccountID = "sys_admin"
	maxUserRequestLimit     = 1_000_000_000
)

var userRequestLimitSettingKeys = [...]string{
	"gatewayUserRequestLimitPerMinute",
	"gatewayUserRequestLimitPerDay",
	"gatewayUserRequestLimitPerWeek",
	"gatewayUserRequestLimitPerMonth",
	"usageStatsTimezone",
}

// ProfileResponse mirrors Node's profile payload, including the derived
// effective request limits required by the frontend profile contract.
type ProfileResponse struct {
	AccountSummary
	EffectiveRequestLimits EffectiveUserRequestLimits `json:"effectiveRequestLimits"`
	// 2026-10-24 用户裁决：个人信息「账户上限」卡片所需的两个生效值，
	// 解析语义与各自执行侧一致（AI 上限照 accounts/write.go 的
	// effectiveAiAccountCreationLimit；特供名额照 accounts/expedited_recovery.go
	// 的内置默认 3），管理面、个人信息与执行侧不各自复制规则。
	EffectiveAiAccountLimit        EffectiveUserRequestLimitValue `json:"effectiveAiAccountLimit"`
	EffectiveExpeditedAccountLimit EffectiveUserRequestLimitValue `json:"effectiveExpeditedAccountLimit"`
}

type EffectiveUserRequestLimits struct {
	PerMinute         EffectiveUserRequestLimitValue `json:"perMinute"`
	PerDay            EffectiveUserRequestLimitValue `json:"perDay"`
	PerWeek           EffectiveUserRequestLimitValue `json:"perWeek"`
	PerMonth          EffectiveUserRequestLimitValue `json:"perMonth"`
	Timezone          string                         `json:"timezone"`
	OverrideExpiresOn *string                        `json:"overrideExpiresOn,omitempty"`
	OverrideActive    bool                           `json:"overrideActive"`
}

type EffectiveUserRequestLimitValue struct {
	Limit  int    `json:"limit"`
	Source string `json:"source"`
}

type userRequestLimitSettings struct {
	PerMinute int
	PerDay    int
	PerWeek   int
	PerMonth  int
	Timezone  string
}

func (d *Deps) effectiveUserRequestLimits(ctx context.Context, overrides *UserRequestLimits) (EffectiveUserRequestLimits, error) {
	if d == nil || d.Settings == nil {
		return EffectiveUserRequestLimits{}, errors.New("auth profile settings are not initialized")
	}
	ctx = ensureCtx(ctx)
	settings, err := d.loadUserRequestLimitSettings(ctx)
	if err != nil {
		return EffectiveUserRequestLimits{}, err
	}
	normalizedOverrides := normalizedProfileRequestLimitOverrides(overrides)
	active, err := userRequestLimitOverrideActive(normalizedOverrides, settings.Timezone, d.now())
	if err != nil {
		return EffectiveUserRequestLimits{}, err
	}
	effectiveOverrides := normalizedOverrides
	if !active {
		effectiveOverrides = nil
	}
	return EffectiveUserRequestLimits{
		PerMinute:         effectiveLimit(settings.PerMinute, effectiveOverrides, overridesFieldPerMinute),
		PerDay:            effectiveLimit(settings.PerDay, effectiveOverrides, overridesFieldPerDay),
		PerWeek:           effectiveLimit(settings.PerWeek, effectiveOverrides, overridesFieldPerWeek),
		PerMonth:          effectiveLimit(settings.PerMonth, effectiveOverrides, overridesFieldPerMonth),
		Timezone:          settings.Timezone,
		OverrideExpiresOn: overrideExpiresOn(normalizedOverrides),
		OverrideActive:    active,
	}, nil
}

// profile 侧账户上限常量镜像执行侧：defaultUserAiAccountLimit（accounts/write.go，
// userAiAccountLimit 设置 schema 缺省）与 defaultExpeditedAccountLimit
// （accounts/expedited_recovery.go，特供名额内置默认）。
const (
	profileDefaultUserAiAccountLimit    = 100
	profileDefaultExpeditedAccountLimit = 3
	maxProfileAiAccountLimit            = 1_000_000
)

const profileUserAiAccountLimitSettingKey = "userAiAccountLimit"

// effectiveAccountLimits 解析个人信息两个账户上限生效值：用户覆盖优先并标记
// source=user，否则回退全局并标记 source=global。AI 上限的越界判定照执行侧
// effectiveAiAccountCreationLimit（[0, 1_000_000] 之外为配置无效）。
func (d *Deps) effectiveAccountLimits(ctx context.Context, aiOverride, expeditedOverride *int) (EffectiveUserRequestLimitValue, EffectiveUserRequestLimitValue, error) {
	aiLimit, err := d.effectiveProfileAiAccountLimit(ctx, aiOverride)
	if err != nil {
		return EffectiveUserRequestLimitValue{}, EffectiveUserRequestLimitValue{}, err
	}
	return aiLimit, effectiveProfileExpeditedAccountLimit(expeditedOverride), nil
}

func (d *Deps) effectiveProfileAiAccountLimit(ctx context.Context, override *int) (EffectiveUserRequestLimitValue, error) {
	if override != nil {
		if *override < 0 || *override > maxProfileAiAccountLimit {
			return EffectiveUserRequestLimitValue{}, errors.New("AI 账户数量限制配置无效")
		}
		return EffectiveUserRequestLimitValue{Limit: *override, Source: "user"}, nil
	}
	if d.Settings != nil {
		setting, ok, err := d.Settings.GetSystem(ctx, systemSettingsAccountID, profileUserAiAccountLimitSettingKey)
		if err != nil {
			return EffectiveUserRequestLimitValue{}, err
		}
		if ok && strings.TrimSpace(setting.ValueJSON) != "" {
			var number json.Number
			if parseErr := json.Unmarshal([]byte(strings.TrimSpace(setting.ValueJSON)), &number); parseErr != nil {
				return EffectiveUserRequestLimitValue{}, errors.New("AI 账户数量限制配置无效")
			}
			value, convErr := strconv.ParseFloat(string(number), 64)
			if convErr != nil || value < 0 || value > float64(maxProfileAiAccountLimit) || math.Trunc(value) != value {
				return EffectiveUserRequestLimitValue{}, errors.New("AI 账户数量限制配置无效")
			}
			return EffectiveUserRequestLimitValue{Limit: int(value), Source: "global"}, nil
		}
	}
	return EffectiveUserRequestLimitValue{Limit: profileDefaultUserAiAccountLimit, Source: "global"}, nil
}

func effectiveProfileExpeditedAccountLimit(override *int) EffectiveUserRequestLimitValue {
	if override != nil {
		return EffectiveUserRequestLimitValue{Limit: *override, Source: "user"}
	}
	return EffectiveUserRequestLimitValue{Limit: profileDefaultExpeditedAccountLimit, Source: "global"}
}

func (d *Deps) loadUserRequestLimitSettings(ctx context.Context) (userRequestLimitSettings, error) {
	values := make(map[string]string, len(userRequestLimitSettingKeys))
	for index, key := range userRequestLimitSettingKeys {
		setting, ok, err := d.Settings.GetSystem(ctx, systemSettingsAccountID, key)
		if err != nil {
			return userRequestLimitSettings{}, err
		}
		if !ok {
			if index < 4 {
				values[key] = "0"
				continue
			}
			return userRequestLimitSettings{}, errors.New("required user request limit setting is missing: " + key)
		}
		values[key] = setting.ValueJSON
	}
	perMinute, err := parseLimitSetting(values[userRequestLimitSettingKeys[0]])
	if err != nil {
		return userRequestLimitSettings{}, err
	}
	perDay, err := parseLimitSetting(values[userRequestLimitSettingKeys[1]])
	if err != nil {
		return userRequestLimitSettings{}, err
	}
	perWeek, err := parseLimitSetting(values[userRequestLimitSettingKeys[2]])
	if err != nil {
		return userRequestLimitSettings{}, err
	}
	perMonth, err := parseLimitSetting(values[userRequestLimitSettingKeys[3]])
	if err != nil {
		return userRequestLimitSettings{}, err
	}
	var timezone string
	if err := json.Unmarshal([]byte(values[userRequestLimitSettingKeys[4]]), &timezone); err != nil {
		return userRequestLimitSettings{}, err
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return userRequestLimitSettings{}, errors.New("usageStatsTimezone is empty")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return userRequestLimitSettings{}, err
	}
	return userRequestLimitSettings{PerMinute: perMinute, PerDay: perDay, PerWeek: perWeek, PerMonth: perMonth, Timezone: timezone}, nil
}

func parseLimitSetting(raw string) (int, error) {
	var number json.Number
	if err := json.Unmarshal([]byte(raw), &number); err != nil {
		return 0, err
	}
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < 0 || value > maxUserRequestLimit {
		return 0, errors.New("user request limit setting is invalid")
	}
	return int(value), nil
}

type overrideField uint8

const (
	overridesFieldPerMinute overrideField = iota
	overridesFieldPerDay
	overridesFieldPerWeek
	overridesFieldPerMonth
)

func effectiveLimit(global int, overrides *UserRequestLimits, field overrideField) EffectiveUserRequestLimitValue {
	value := global
	source := "global"
	if overrides != nil {
		var candidate *int
		switch field {
		case overridesFieldPerMinute:
			candidate = overrides.PerMinute
		case overridesFieldPerDay:
			candidate = overrides.PerDay
		case overridesFieldPerWeek:
			candidate = overrides.PerWeek
		case overridesFieldPerMonth:
			candidate = overrides.PerMonth
		}
		if candidate != nil {
			value = *candidate
			source = "user"
		}
	}
	return EffectiveUserRequestLimitValue{Limit: value, Source: source}
}

func overrideExpiresOn(overrides *UserRequestLimits) *string {
	if overrides == nil || overrides.ExpiresOn == nil || *overrides.ExpiresOn == "" {
		return nil
	}
	return overrides.ExpiresOn
}

func normalizedProfileRequestLimitOverrides(overrides *UserRequestLimits) *UserRequestLimits {
	if overrides == nil || (overrides.PerMinute == nil && overrides.PerDay == nil && overrides.PerWeek == nil && overrides.PerMonth == nil) {
		return nil
	}
	for _, window := range []*int{overrides.PerMinute, overrides.PerDay, overrides.PerWeek, overrides.PerMonth} {
		if window != nil && (*window < 0 || *window > maxUserRequestLimit) {
			return nil
		}
	}
	normalized := *overrides
	if normalized.ExpiresOn == nil || *normalized.ExpiresOn == "" {
		normalized.ExpiresOn = nil
		return &normalized
	}
	if !validUserRequestLimitExpiresOn(*normalized.ExpiresOn) {
		return nil
	}
	return &normalized
}

func validUserRequestLimitExpiresOn(value string) bool {
	if len(value) != len("2006-01-02") || value[:4] < "0100" {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func userRequestLimitOverrideActive(overrides *UserRequestLimits, timezone string, now time.Time) (bool, error) {
	if overrides == nil {
		return false, nil
	}
	if overrides.ExpiresOn == nil || *overrides.ExpiresOn == "" {
		return true, nil
	}
	if !validUserRequestLimitExpiresOn(*overrides.ExpiresOn) {
		return false, errors.New("expiresOn is invalid")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return false, err
	}
	return now.In(location).Format("2006-01-02") <= *overrides.ExpiresOn, nil
}

func (d *Deps) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}
