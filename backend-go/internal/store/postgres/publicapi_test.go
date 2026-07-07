package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestPublicAPIAuthRecordFromRow(t *testing.T) {
	expiresAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.FixedZone("test", 8*3600))
	lastUsedAt := expiresAt.Add(-time.Hour)

	record, err := publicAPIAuthRecordFromRow(postgresqueries.FindPublicAPIAuthTokenByHashRow{
		SourceRefID:          "source_1",
		SourceName:           "source",
		SourceStatus:         "active",
		SourceScopesJson:     `["scope:a","scope:b"]`,
		SourceRateLimitsJson: `[{"windowSeconds":60,"maxRequests":10}]`,
		SourceExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
		SourceLastUsedAt:     pgtype.Timestamptz{Time: lastUsedAt, Valid: true},
		TokenID:              "token_1",
		TokenName:            "token",
		TokenPrefix:          "juis_abc",
		TokenStatus:          "active",
		TokenScopesJson:      `["scope:b"]`,
	})
	if err != nil {
		t.Fatalf("publicAPIAuthRecordFromRow() error = %v", err)
	}

	if record.SourceRefID != "source_1" || record.TokenID != "token_1" {
		t.Fatalf("record ids = %+v", record)
	}
	if got, want := strings.Join(record.SourceScopes, ","), "scope:a,scope:b"; got != want {
		t.Fatalf("source scopes = %q, want %q", got, want)
	}
	if got, want := len(record.SourceRateLimits), 1; got != want {
		t.Fatalf("rate limits length = %d, want %d", got, want)
	}
	if record.SourceRateLimits[0].WindowSeconds != 60 || record.SourceRateLimits[0].MaxRequests != 10 {
		t.Fatalf("rate limit = %+v", record.SourceRateLimits[0])
	}
	if record.SourceExpiresAt == nil || !record.SourceExpiresAt.Equal(expiresAt.UTC()) {
		t.Fatalf("source expires at = %v, want %v", record.SourceExpiresAt, expiresAt.UTC())
	}
	if record.SourceLastUsedAt == nil || !record.SourceLastUsedAt.Equal(lastUsedAt.UTC()) {
		t.Fatalf("source last used at = %v, want %v", record.SourceLastUsedAt, lastUsedAt.UTC())
	}
	if record.TokenExpiresAt != nil || record.TokenLastUsedAt != nil {
		t.Fatalf("token optional times = %v / %v, want nil", record.TokenExpiresAt, record.TokenLastUsedAt)
	}
}

func TestPublicAPIAuthRecordFromRowRejectsMalformedJSON(t *testing.T) {
	if _, err := publicAPIAuthRecordFromRow(postgresqueries.FindPublicAPIAuthTokenByHashRow{
		SourceScopesJson:     `{"bad":true}`,
		SourceRateLimitsJson: `[]`,
		TokenScopesJson:      `[]`,
	}); err == nil {
		t.Fatal("publicAPIAuthRecordFromRow() error = nil, want malformed source scopes error")
	}
	if _, err := publicAPIAuthRecordFromRow(postgresqueries.FindPublicAPIAuthTokenByHashRow{
		SourceScopesJson:     `[]`,
		SourceRateLimitsJson: `[{"windowSeconds":0,"maxRequests":1}]`,
		TokenScopesJson:      `[]`,
	}); err == nil {
		t.Fatal("publicAPIAuthRecordFromRow() error = nil, want invalid rate limit error")
	}
}

func TestInsertPublicAPILogParamsNormalizesInput(t *testing.T) {
	statusCode := 201
	durationMs := int64(123)
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(250 * time.Millisecond)

	params, err := insertPublicAPILogParams(port.PublicAPILogInput{
		ID:                    "publog_1",
		TraceID:               "trace_1",
		Method:                "GET",
		Path:                  "/__aipublic__/group/list",
		StatusCode:            &statusCode,
		Success:               true,
		DurationMs:            &durationMs,
		RequestSizeBytes:      -10,
		ResponseSizeBytes:     20,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		RequestData:           map[string]any{"query": map[string]any{"targetUsername": "admin"}},
		ResponseData:          map[string]any{"body": map[string]any{"ok": true}},
		StartedAt:             startedAt,
		EndedAt:               endedAt,
	})
	if err != nil {
		t.Fatalf("insertPublicAPILogParams() error = %v", err)
	}

	if !params.TraceID.Valid || params.TraceID.String != "trace_1" {
		t.Fatalf("trace id = %+v", params.TraceID)
	}
	if params.SourceRefID.Valid {
		t.Fatalf("empty source ref id should be NULL: %+v", params.SourceRefID)
	}
	if !params.StatusCode.Valid || params.StatusCode.Int32 != int32(statusCode) {
		t.Fatalf("status code = %+v", params.StatusCode)
	}
	if !params.DurationMs.Valid || params.DurationMs.Int64 != durationMs {
		t.Fatalf("duration = %+v", params.DurationMs)
	}
	if params.RequestSizeBytes != 0 || params.ResponseSizeBytes != 20 {
		t.Fatalf("sizes = %d/%d", params.RequestSizeBytes, params.ResponseSizeBytes)
	}
	if params.RequestCaptureStatus != string(port.PublicAPILogCaptureEmpty) {
		t.Fatalf("request capture status = %q", params.RequestCaptureStatus)
	}
	if params.ResponseCaptureStatus != string(port.PublicAPILogCaptureComplete) {
		t.Fatalf("response capture status = %q", params.ResponseCaptureStatus)
	}
	if !params.CreatedAt.Time.Equal(endedAt) {
		t.Fatalf("created at = %v, want ended at %v", params.CreatedAt.Time, endedAt)
	}

	var requestData map[string]any
	if err := json.Unmarshal([]byte(params.RequestDataJson), &requestData); err != nil {
		t.Fatalf("request data json invalid: %v", err)
	}
	if _, ok := requestData["query"]; !ok {
		t.Fatalf("request data json = %s", params.RequestDataJson)
	}
}

func TestInsertPublicAPILogParamsRejectsRequiredFields(t *testing.T) {
	startedAt := time.Now()
	endedAt := startedAt.Add(time.Millisecond)
	if _, err := insertPublicAPILogParams(port.PublicAPILogInput{
		Method:    "GET",
		Path:      "/__aipublic__/group/list",
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}); err == nil {
		t.Fatal("insertPublicAPILogParams() error = nil, want missing id error")
	}
	if _, err := insertPublicAPILogParams(port.PublicAPILogInput{
		ID:        "publog_1",
		Path:      "/__aipublic__/group/list",
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}); err == nil {
		t.Fatal("insertPublicAPILogParams() error = nil, want missing method error")
	}
}

func TestSafeJSONObjectStringFallsBackOnMarshalFailure(t *testing.T) {
	got := safeJSONObjectString(map[string]any{
		"bad": func() {},
	})
	if got != "{}" {
		t.Fatalf("safeJSONObjectString() = %q, want {}", got)
	}
}
