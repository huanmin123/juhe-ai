package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type Store struct {
	pool *pgxpool.Pool
}

type Reader interface {
	ListBaselineSchemas(ctx context.Context, names []string) ([]string, error)
}

type systemAPIClientIPAllowlistQuerier interface {
	SystemAPIClientIPAllowlisted(ctx context.Context, arg postgresqueries.SystemAPIClientIPAllowlistedParams) (bool, error)
}

type TxFunc func(ctx context.Context, q Reader) error

func Open(ctx context.Context, rawURL string) (*Store, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("postgres url is required")
	}

	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) ListBaselineSchemas(ctx context.Context, names []string) ([]string, error) {
	return s.queries().ListBaselineSchemas(ctx, names)
}

func (s *Store) PublicGlobalSettings(ctx context.Context) (port.PublicGlobalSettings, error) {
	rows, err := s.queries().ListPublicGlobalSettings(ctx)
	if err != nil {
		return port.PublicGlobalSettings{}, fmt.Errorf("list public global settings: %w", err)
	}

	values := map[string]string{}
	for _, row := range rows {
		value, err := parsePublicSettingValue(row.ValueJson, row.Key)
		if err != nil {
			return port.PublicGlobalSettings{}, err
		}
		values[row.Key] = value
	}

	appName, ok := values["appName"]
	if !ok {
		return port.PublicGlobalSettings{}, fmt.Errorf("全局设置缺少字段：appName")
	}
	appIcon, ok := values["appIcon"]
	if !ok {
		return port.PublicGlobalSettings{}, fmt.Errorf("全局设置缺少字段：appIcon")
	}

	return port.PublicGlobalSettings{
		AppName: appName,
		AppIcon: appIcon,
	}, nil
}

func (s *Store) SystemAPIRateLimitSettings(ctx context.Context) (port.SystemAPIRateLimitSettings, error) {
	rows, err := s.queries().ListSystemAPIRateLimitSettings(ctx)
	if err != nil {
		return port.SystemAPIRateLimitSettings{}, fmt.Errorf("list system api rate limit settings: %w", err)
	}

	values := map[string]int{}
	for _, row := range rows {
		value, err := parseIntegerSettingValue(row.ValueJson, row.Key, 0, 1_000_000)
		if err != nil {
			return port.SystemAPIRateLimitSettings{}, err
		}
		values[row.Key] = value
	}

	requiredKeys := []string{
		"systemApiRateLimitIpReadPerMinute",
		"systemApiRateLimitIpReadBurstPer10Seconds",
		"systemApiRateLimitIpWritePerMinute",
		"systemApiRateLimitIpWriteBurstPer10Seconds",
		"systemApiRateLimitUserReadPerMinute",
		"systemApiRateLimitUserWritePerMinute",
	}
	for _, key := range requiredKeys {
		if _, ok := values[key]; !ok {
			return port.SystemAPIRateLimitSettings{}, fmt.Errorf("系统设置缺少字段：%s", key)
		}
	}

	return port.SystemAPIRateLimitSettings{
		IPReadPerMinute:          values["systemApiRateLimitIpReadPerMinute"],
		IPReadBurstPer10Seconds:  values["systemApiRateLimitIpReadBurstPer10Seconds"],
		IPWritePerMinute:         values["systemApiRateLimitIpWritePerMinute"],
		IPWriteBurstPer10Seconds: values["systemApiRateLimitIpWriteBurstPer10Seconds"],
		UserReadPerMinute:        values["systemApiRateLimitUserReadPerMinute"],
		UserWritePerMinute:       values["systemApiRateLimitUserWritePerMinute"],
	}, nil
}

func (s *Store) SystemAPIClientIPAllowlisted(ctx context.Context, ipHash string, now time.Time) (bool, error) {
	return systemAPIClientIPAllowlisted(ctx, s.queries(), ipHash, now)
}

func systemAPIClientIPAllowlisted(
	ctx context.Context,
	q systemAPIClientIPAllowlistQuerier,
	ipHash string,
	now time.Time,
) (bool, error) {
	allowlisted, err := q.SystemAPIClientIPAllowlisted(ctx, postgresqueries.SystemAPIClientIPAllowlistedParams{
		IpHash: ipHash,
		NowAt:  now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return false, fmt.Errorf("check system api client IP allowlist: %w", err)
	}
	return allowlisted, nil
}

func (s *Store) queries() *postgresqueries.Queries {
	return postgresqueries.New(s.pool)
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) InTx(ctx context.Context, fn TxFunc) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin postgres tx: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	if err := fn(ctx, s.queries().WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit postgres tx rolled back: %w", err)
		}
		return fmt.Errorf("commit postgres tx: %w", err)
	}
	committed = true
	return nil
}

func parsePublicSettingValue(raw string, key string) (string, error) {
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("%s 必须是 JSON 字符串: %w", key, err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s 必须是非空字符串", key)
	}
	return value, nil
}

func parseIntegerSettingValue(raw string, key string, min int, max int) (int, error) {
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, fmt.Errorf("%s 必须是 JSON 整数: %w", key, err)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间", key, min, max)
	}
	return value, nil
}
