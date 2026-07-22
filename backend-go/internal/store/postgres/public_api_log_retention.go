package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) GetPublicAPILogRetentionDays(ctx context.Context) (int, bool, error) {
	raw, err := s.queries().GetPublicAPILogRetentionDays(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("读取公开接口日志保留天数失败: %w", err)
	}
	value, err := parsePublicAPILogRetentionDays(raw)
	if err != nil {
		return 0, false, err
	}
	return value, true, nil
}

func parsePublicAPILogRetentionDays(raw string) (int, error) {
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, fmt.Errorf("publicApiLogRetentionDays JSON 无效: %w", err)
	}
	if value < 1 || value > 365 {
		return 0, fmt.Errorf("系统设置 publicApiLogRetentionDays 必须在 1 到 365 之间")
	}
	return value, nil
}

func (s *Store) CleanupPublicAPILogsBefore(ctx context.Context, input port.PublicAPILogCleanupInput) (int64, error) {
	cutoff := input.CutoffCreatedAt
	if cutoff.IsZero() {
		return 0, fmt.Errorf("公开接口日志保留清理 cutoff_created_at 不能为空")
	}
	if input.Limit <= 0 {
		return 0, fmt.Errorf("公开接口日志保留清理 limit 必须大于 0")
	}
	deleted, err := s.queries().CleanupPublicAPILogsBefore(ctx, postgresqueries.CleanupPublicAPILogsBeforeParams{
		CutoffCreatedAt: pgTimestamptz(cutoff.UTC()),
		RowLimit:        int32(input.Limit),
	})
	if err != nil {
		return 0, fmt.Errorf("按保留期清理公开接口日志失败: %w", err)
	}
	return deleted, nil
}

var _ port.PublicAPILogRetentionCleaner = (*Store)(nil)
