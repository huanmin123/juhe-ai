package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func (s *Store) FindPublicAPIAuthTokenByHash(ctx context.Context, tokenHash string) (port.PublicAPIAuthRecord, bool, error) {
	row, err := s.queries().FindPublicAPIAuthTokenByHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.PublicAPIAuthRecord{}, false, nil
	}
	if err != nil {
		return port.PublicAPIAuthRecord{}, false, fmt.Errorf("find public api auth token by hash: %w", err)
	}

	record, err := publicAPIAuthRecordFromRow(row)
	if err != nil {
		return port.PublicAPIAuthRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) TouchPublicAPIAuthLastUsed(ctx context.Context, touch port.PublicAPIAuthLastUsedTouch) error {
	if touch.TouchToken {
		if err := s.queries().TouchPublicAPIAuthTokenLastUsed(ctx, postgresqueries.TouchPublicAPIAuthTokenLastUsedParams{
			ID:         touch.TokenID,
			LastUsedAt: pgTimestamptz(touch.Now),
		}); err != nil {
			return fmt.Errorf("touch public api token last_used_at: %w", err)
		}
	}
	if touch.TouchSource {
		if err := s.queries().TouchPublicAPIAuthSourceLastUsed(ctx, postgresqueries.TouchPublicAPIAuthSourceLastUsedParams{
			ID:         touch.SourceRefID,
			LastUsedAt: pgTimestamptz(touch.Now),
		}); err != nil {
			return fmt.Errorf("touch public api source last_used_at: %w", err)
		}
	}
	return nil
}

func (s *Store) InsertPublicAPILog(ctx context.Context, input port.PublicAPILogInput) error {
	params, err := insertPublicAPILogParams(input)
	if err != nil {
		return err
	}
	if err := s.queries().InsertPublicAPILog(ctx, params); err != nil {
		return fmt.Errorf("insert public api log: %w", err)
	}
	return nil
}

func publicAPIAuthRecordFromRow(row postgresqueries.FindPublicAPIAuthTokenByHashRow) (port.PublicAPIAuthRecord, error) {
	sourceScopes, err := decodeStringArrayJSON(row.SourceScopesJson, "source scopes_json")
	if err != nil {
		return port.PublicAPIAuthRecord{}, err
	}
	tokenScopes, err := decodeStringArrayJSON(row.TokenScopesJson, "token scopes_json")
	if err != nil {
		return port.PublicAPIAuthRecord{}, err
	}
	rateLimits, err := decodePublicAPIRateLimitsJSON(row.SourceRateLimitsJson)
	if err != nil {
		return port.PublicAPIAuthRecord{}, err
	}

	return port.PublicAPIAuthRecord{
		SourceRefID:      row.SourceRefID,
		SourceName:       row.SourceName,
		SourceStatus:     row.SourceStatus,
		SourceScopes:     sourceScopes,
		SourceRateLimits: rateLimits,
		SourceExpiresAt:  timestamptzPtr(row.SourceExpiresAt),
		SourceLastUsedAt: timestamptzPtr(row.SourceLastUsedAt),
		TokenID:          row.TokenID,
		TokenName:        row.TokenName,
		TokenPrefix:      row.TokenPrefix,
		TokenStatus:      row.TokenStatus,
		TokenScopes:      tokenScopes,
		TokenExpiresAt:   timestamptzPtr(row.TokenExpiresAt),
		TokenLastUsedAt:  timestamptzPtr(row.TokenLastUsedAt),
	}, nil
}

func insertPublicAPILogParams(input port.PublicAPILogInput) (postgresqueries.InsertPublicAPILogParams, error) {
	if input.ID == "" {
		return postgresqueries.InsertPublicAPILogParams{}, fmt.Errorf("public api log id is required")
	}
	if input.Method == "" {
		return postgresqueries.InsertPublicAPILogParams{}, fmt.Errorf("public api log method is required")
	}
	if input.Path == "" {
		return postgresqueries.InsertPublicAPILogParams{}, fmt.Errorf("public api log path is required")
	}
	if input.StartedAt.IsZero() {
		return postgresqueries.InsertPublicAPILogParams{}, fmt.Errorf("public api log started_at is required")
	}
	if input.EndedAt.IsZero() {
		return postgresqueries.InsertPublicAPILogParams{}, fmt.Errorf("public api log ended_at is required")
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = input.EndedAt
	}

	return postgresqueries.InsertPublicAPILogParams{
		ID:                    input.ID,
		TraceID:               pgText(input.TraceID),
		SourceRefID:           pgText(input.SourceRefID),
		SourceName:            pgText(input.SourceName),
		TokenID:               pgText(input.TokenID),
		TokenName:             pgText(input.TokenName),
		TokenPrefix:           pgText(input.TokenPrefix),
		IsTestToken:           input.IsTestToken,
		Method:                input.Method,
		Path:                  input.Path,
		QueryString:           pgText(input.QueryString),
		ClientIp:              pgText(input.ClientIP),
		UserAgent:             pgText(input.UserAgent),
		StatusCode:            pgInt4Ptr(input.StatusCode),
		Success:               input.Success,
		DurationMs:            pgInt8Ptr(input.DurationMs),
		RequestSizeBytes:      max(0, input.RequestSizeBytes),
		ResponseSizeBytes:     max(0, input.ResponseSizeBytes),
		RequestCaptureStatus:  normalizePublicAPILogCaptureStatus(input.RequestCaptureStatus),
		ResponseCaptureStatus: normalizePublicAPILogCaptureStatus(input.ResponseCaptureStatus),
		RequestDataJson:       safeJSONObjectString(input.RequestData),
		ResponseDataJson:      safeJSONObjectString(input.ResponseData),
		ErrorCode:             pgText(input.ErrorCode),
		ErrorMessage:          pgText(input.ErrorMessage),
		StartedAt:             pgTimestamptz(input.StartedAt),
		EndedAt:               pgTimestamptz(input.EndedAt),
		CreatedAt:             pgTimestamptz(createdAt),
	}, nil
}

func decodeStringArrayJSON(raw string, label string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%s must be a JSON string array: %w", label, err)
	}
	if values == nil {
		return []string{}, nil
	}
	return values, nil
}

func decodePublicAPIRateLimitsJSON(raw string) ([]port.PublicAPIRateLimitRule, error) {
	if raw == "" {
		return []port.PublicAPIRateLimitRule{}, nil
	}
	var rules []port.PublicAPIRateLimitRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("source rate_limits_json must be a JSON array: %w", err)
	}
	if rules == nil {
		return []port.PublicAPIRateLimitRule{}, nil
	}
	for _, rule := range rules {
		if rule.WindowSeconds <= 0 || rule.MaxRequests <= 0 {
			return nil, fmt.Errorf("source rate_limits_json contains invalid rule")
		}
	}
	return rules, nil
}

func normalizePublicAPILogCaptureStatus(value port.PublicAPILogCaptureStatus) string {
	switch value {
	case port.PublicAPILogCaptureComplete,
		port.PublicAPILogCaptureTruncated,
		port.PublicAPILogCaptureEmpty,
		port.PublicAPILogCaptureDropped:
		return string(value)
	default:
		return string(port.PublicAPILogCaptureEmpty)
	}
}

func safeJSONObjectString(value map[string]any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func pgText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func pgInt4Ptr(value *int) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*value), Valid: true}
}

func pgInt8Ptr(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func pgTimestamptz(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func timestamptzPtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}
