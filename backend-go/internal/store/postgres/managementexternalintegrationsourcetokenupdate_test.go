package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementExternalIntegrationSourceTokenUpdateSQLContract(t *testing.T) {
	if !strings.Contains(managementExternalIntegrationSourceTokenUpdateSourceLockSQL, "WHERE sources.id = $1::text\nFOR UPDATE") {
		t.Fatalf("source lock SQL must lock by source id:\n%s", managementExternalIntegrationSourceTokenUpdateSourceLockSQL)
	}
	if !strings.Contains(managementExternalIntegrationSourceTokenUpdateTokenLockSQL, "WHERE tokens.source_ref_id = $1::text\n  AND tokens.id = $2::text\nFOR UPDATE") {
		t.Fatalf("token lock SQL must lock by source_ref_id and token id:\n%s", managementExternalIntegrationSourceTokenUpdateTokenLockSQL)
	}
	if !strings.Contains(managementExternalIntegrationSourceTokenUpdateSQL, "WHERE source_ref_id = $7::text\n  AND id = $8::text\nRETURNING") {
		t.Fatalf("update SQL must use the exact source_ref_id and id predicate:\n%s", managementExternalIntegrationSourceTokenUpdateSQL)
	}
	for name, sql := range map[string]string{
		"token lock": managementExternalIntegrationSourceTokenUpdateTokenLockSQL,
		"update":     managementExternalIntegrationSourceTokenUpdateSQL,
	} {
		if forbidden, found := managementExternalIntegrationSourceTokenUpdateSQLForbiddenField(sql); found {
			t.Fatalf("%s SQL exposes forbidden field %q:\n%s", name, forbidden, sql)
		}
	}
}

func TestManagementExternalIntegrationSourceTokenUpdateSQLGateRejectsSelectStarCaseInsensitively(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM juhe_business.external_integration_source_tokens",
		"select * from juhe_business.external_integration_source_tokens",
		"SeLeCt * FrOm juhe_business.external_integration_source_tokens",
	} {
		forbidden, found := managementExternalIntegrationSourceTokenUpdateSQLForbiddenField(sql)
		if !found || forbidden != "select *" {
			t.Fatalf("SQL gate result for %q = %q/%t, want select */true", sql, forbidden, found)
		}
	}
}

func managementExternalIntegrationSourceTokenUpdateSQLForbiddenField(sql string) (string, bool) {
	lowerSQL := strings.ToLower(sql)
	for _, forbidden := range []string{"token_hash", "token_secret_encrypted", "select *"} {
		if strings.Contains(lowerSQL, forbidden) {
			return forbidden, true
		}
	}
	return "", false
}

func TestUpdateManagementExternalIntegrationSourceTokenLocksMergesUpdatesMapsAndValidates(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 13, 14, 123456789, time.FixedZone("UTC+8", 8*60*60))
	createdAt := now.Add(-48 * time.Hour).UTC()
	lastUsedAt := now.Add(-time.Hour).UTC()
	expiresAt := now.Add(24 * time.Hour).UTC()
	newExpiresAt := now.Add(72 * time.Hour).UTC()
	before := managementExternalIntegrationSourceTokenUpdateTestRow(
		"source_1",
		"token_1",
		"Old Token",
		"active",
		`["api_keys:read"]`,
		&expiresAt,
		&lastUsedAt,
		createdAt,
		now.Add(-time.Hour).UTC(),
		nil,
	)
	after := before
	after.Name = "New Token"
	after.Status = "disabled"
	after.ScopesJSON = `["api_keys:write"]`
	after.ExpiresAt = pgTimestamptzPtr(&newExpiresAt)
	after.UpdatedAt = pgTimestamptz(now)

	tx := &managementExternalIntegrationSourceTokenUpdateTxStub{
		rows: []pgx.Row{
			managementExternalIntegrationSourceTokenUpdateRow("source_1"),
			managementExternalIntegrationSourceTokenUpdateTokenRow(before),
			managementExternalIntegrationSourceTokenUpdateTokenRow(after),
		},
	}
	validateCalls := 0
	result, err := updateManagementExternalIntegrationSourceTokenInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ManagementExternalIntegrationSourceTokenUpdateInput{
			SourceID:     "source_1",
			TokenID:      "token_1",
			HasName:      true,
			Name:         "New Token",
			HasStatus:    true,
			Status:       "disabled",
			HasScopes:    true,
			ScopesJSON:   `["api_keys:write"]`,
			HasExpiresAt: true,
			ExpiresAt:    &newExpiresAt,
			UpdatedAt:    now,
		},
		func(got port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
			validateCalls++
			if tx.commitCalls != 0 {
				t.Fatal("validate ran after commit")
			}
			if got.BeforeToken.ID != "token_1" || got.AfterToken.Name != "New Token" {
				t.Fatalf("validate result = %#v", got)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("update token: %v", err)
	}
	if got, want := tx.calls, []string{"source-lock", "token-lock", "update", "commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("transaction order = %#v, want %#v", got, want)
	}
	if validateCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("validate/commit/rollback = %d/%d/%d, want 1/1/0", validateCalls, tx.commitCalls, tx.rollbackCalls)
	}
	wantArgs := []any{
		"New Token",
		"disabled",
		`{"mode":"strict","scopes":["new:write"]}`,
		pgTimestamptz(newExpiresAt),
		pgTimestamptz(now),
		pgtype.Timestamptz{},
		"source_1",
		"token_1",
	}
	if !reflect.DeepEqual(tx.updateArgs, wantArgs) {
		t.Fatalf("update args = %#v, want %#v", tx.updateArgs, wantArgs)
	}
	if result.BeforeToken.ScopesJSON != `["api_keys:read"]` || result.AfterToken.ScopesJSON != `["api_keys:write"]` {
		t.Fatalf("JSON mapping = before %q after %q", result.BeforeToken.ScopesJSON, result.AfterToken.ScopesJSON)
	}
	managementExternalIntegrationSourceTokenUpdateAssertScopesJSONArray(
		t,
		result.BeforeToken.ScopesJSON,
		[]string{"api_keys:read"},
	)
	managementExternalIntegrationSourceTokenUpdateAssertScopesJSONArray(
		t,
		result.AfterToken.ScopesJSON,
		[]string{"api_keys:write"},
	)
	if result.BeforeToken.ExpiresAt == nil || !result.BeforeToken.ExpiresAt.Equal(expiresAt) ||
		result.BeforeToken.LastUsedAt == nil || !result.BeforeToken.LastUsedAt.Equal(lastUsedAt) ||
		result.BeforeToken.RevokedAt != nil || result.AfterToken.RevokedAt != nil {
		t.Fatalf("nullable mapping = before %#v after %#v", result.BeforeToken, result.AfterToken)
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenEmptyPatchStillUpdatesTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 17, 5, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	before := managementExternalIntegrationSourceTokenUpdateTestRow(
		"source_1", "token_1", "Token", "active", `[]`, &expiresAt, nil,
		now.Add(-time.Hour), now.Add(-time.Minute), nil,
	)
	tx := &managementExternalIntegrationSourceTokenUpdateTxStub{rows: []pgx.Row{
		managementExternalIntegrationSourceTokenUpdateRow("source_1"),
		managementExternalIntegrationSourceTokenUpdateTokenRow(before),
		managementExternalIntegrationSourceTokenUpdateTokenRow(func() managementExternalIntegrationSourceTokenUpdateRowData {
			after := before
			after.UpdatedAt = pgTimestamptz(now)
			return after
		}()),
	}}

	_, err := updateManagementExternalIntegrationSourceTokenInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ManagementExternalIntegrationSourceTokenUpdateInput{SourceID: "source_1", TokenID: "token_1", UpdatedAt: now},
		func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return nil },
	)
	if err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	wantArgs := []any{
		before.Name,
		before.Status,
		before.ScopesJSON,
		before.ExpiresAt,
		pgTimestamptz(now),
		pgtype.Timestamptz{},
		"source_1",
		"token_1",
	}
	if !reflect.DeepEqual(tx.updateArgs, wantArgs) {
		t.Fatalf("empty patch update args = %#v, want %#v", tx.updateArgs, wantArgs)
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenRevokedAtTransitions(t *testing.T) {
	now := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	previous := now.Add(-time.Hour)
	tests := []struct {
		name            string
		beforeStatus    string
		beforeRevokedAt *time.Time
		hasStatus       bool
		status          string
		wantStatus      string
		wantRevokedAt   *time.Time
	}{
		{name: "non-revoked to revoked sets updated at", beforeStatus: "active", hasStatus: true, status: "revoked", wantStatus: "revoked", wantRevokedAt: &now},
		{name: "already revoked without status preserves existing", beforeStatus: "revoked", beforeRevokedAt: &previous, wantStatus: "revoked", wantRevokedAt: &previous},
		{name: "already revoked to revoked preserves nil", beforeStatus: "revoked", hasStatus: true, status: "revoked", wantStatus: "revoked"},
		{name: "revoked to active clears", beforeStatus: "revoked", beforeRevokedAt: &previous, hasStatus: true, status: "active", wantStatus: "active"},
		{name: "revoked to disabled clears", beforeStatus: "revoked", beforeRevokedAt: &previous, hasStatus: true, status: "disabled", wantStatus: "disabled"},
		{name: "non-revoked without status clears residue", beforeStatus: "active", beforeRevokedAt: &previous, wantStatus: "active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := managementExternalIntegrationSourceTokenUpdateTestRow(
				"source_1", "token_1", "Token", test.beforeStatus, `[]`, nil, nil,
				now.Add(-time.Hour), now.Add(-time.Minute), test.beforeRevokedAt,
			)
			after := before
			after.Status = test.wantStatus
			after.UpdatedAt = pgTimestamptz(now)
			after.RevokedAt = pgTimestamptzPtr(test.wantRevokedAt)
			tx := &managementExternalIntegrationSourceTokenUpdateTxStub{rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateTokenRow(before),
				managementExternalIntegrationSourceTokenUpdateTokenRow(after),
			}}
			result, err := updateManagementExternalIntegrationSourceTokenInTx(
				context.Background(),
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
				port.ManagementExternalIntegrationSourceTokenUpdateInput{
					SourceID: "source_1", TokenID: "token_1", HasStatus: test.hasStatus,
					Status: test.status, UpdatedAt: now,
				},
				func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return nil },
			)
			if err != nil {
				t.Fatalf("update revoked state: %v", err)
			}
			if got := tx.updateArgs[1]; got != test.wantStatus {
				t.Fatalf("status arg = %q, want %q", got, test.wantStatus)
			}
			wantPG := pgTimestamptzPtr(test.wantRevokedAt)
			if got := tx.updateArgs[5]; !reflect.DeepEqual(got, wantPG) {
				t.Fatalf("revoked_at arg = %#v, want %#v", got, wantPG)
			}
			if !optionalTimeEqual(result.AfterToken.RevokedAt, test.wantRevokedAt) {
				t.Fatalf("after revokedAt = %v, want %v", result.AfterToken.RevokedAt, test.wantRevokedAt)
			}
		})
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenMapsMissingAndBuiltInErrors(t *testing.T) {
	tests := []struct {
		name      string
		rows      []pgx.Row
		wantErr   error
		wantCalls []string
	}{
		{
			name:      "source not found",
			rows:      []pgx.Row{managementExternalIntegrationSourceTokenUpdateErrorRow(pgx.ErrNoRows)},
			wantErr:   port.ErrManagementExternalIntegrationSourceNotFound,
			wantCalls: []string{"source-lock"},
		},
		{
			name:      "built-in source",
			rows:      []pgx.Row{managementExternalIntegrationSourceTokenUpdateRow(publicapi.BuiltInTestSourceID)},
			wantErr:   port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted,
			wantCalls: []string{"source-lock"},
		},
		{
			name: "token not found or source mismatch",
			rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateErrorRow(pgx.ErrNoRows),
			},
			wantErr:   port.ErrManagementExternalIntegrationSourceTokenNotFound,
			wantCalls: []string{"source-lock", "token-lock"},
		},
		{
			name: "built-in token",
			rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateTokenRow(managementExternalIntegrationSourceTokenUpdateTestRow(
					"source_1", publicapi.BuiltInTestTokenID, "Built-in", "active", `[]`, nil, nil,
					time.Now().Add(-time.Hour), time.Now(), nil,
				)),
			},
			wantErr:   port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted,
			wantCalls: []string{"source-lock", "token-lock"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &managementExternalIntegrationSourceTokenUpdateTxStub{rows: test.rows}
			_, err := updateManagementExternalIntegrationSourceTokenTx(
				context.Background(),
				tx,
				port.ManagementExternalIntegrationSourceTokenUpdateInput{SourceID: "source_1", TokenID: "token_1", UpdatedAt: time.Now()},
				func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return nil },
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if !reflect.DeepEqual(tx.calls, test.wantCalls) {
				t.Fatalf("calls = %#v, want %#v", tx.calls, test.wantCalls)
			}
		})
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenPreservesOperationErrorsWithContext(t *testing.T) {
	beginErr := errors.New("begin unavailable")
	_, err := updateManagementExternalIntegrationSourceTokenInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, beginErr },
		port.ManagementExternalIntegrationSourceTokenUpdateInput{},
		func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return nil },
	)
	if !errors.Is(err, beginErr) || !strings.Contains(err.Error(), "begin management external integration source token update") {
		t.Fatalf("begin error = %v", err)
	}

	operationErr := errors.New("postgres unavailable")
	tests := []struct {
		name     string
		rows     []pgx.Row
		wantText string
	}{
		{name: "source lock", rows: []pgx.Row{managementExternalIntegrationSourceTokenUpdateErrorRow(operationErr)}, wantText: "lock management external integration source token update source"},
		{name: "token lock", rows: []pgx.Row{managementExternalIntegrationSourceTokenUpdateRow("source_1"), managementExternalIntegrationSourceTokenUpdateErrorRow(operationErr)}, wantText: "lock management external integration source token update token"},
		{name: "update", rows: []pgx.Row{
			managementExternalIntegrationSourceTokenUpdateRow("source_1"),
			managementExternalIntegrationSourceTokenUpdateTokenRow(managementExternalIntegrationSourceTokenUpdateTestRow("source_1", "token_1", "Token", "active", `[]`, nil, nil, time.Now().Add(-time.Hour), time.Now(), nil)),
			managementExternalIntegrationSourceTokenUpdateErrorRow(operationErr),
		}, wantText: "update management external integration source token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &managementExternalIntegrationSourceTokenUpdateTxStub{rows: test.rows}
			_, err := updateManagementExternalIntegrationSourceTokenTx(
				context.Background(), tx,
				port.ManagementExternalIntegrationSourceTokenUpdateInput{SourceID: "source_1", TokenID: "token_1", UpdatedAt: time.Now()},
				func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return nil },
			)
			if !errors.Is(err, operationErr) || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want wrapped %q", err, test.wantText)
			}
		})
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenMappingAndValidationFailuresRollback(t *testing.T) {
	now := time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC)
	valid := managementExternalIntegrationSourceTokenUpdateTestRow(
		"source_1", "token_1", "Token", "active", `["api_keys:read"]`,
		nil, nil, now.Add(-time.Hour), now.Add(-time.Minute), nil,
	)
	validationErr := errors.New("snapshot invalid")
	tests := []struct {
		name          string
		rows          []pgx.Row
		validate      func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error
		wantErr       error
		wantText      string
		wantQueries   int
		wantValidates int
	}{
		{
			name: "before mapping",
			rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateTokenRow(func() managementExternalIntegrationSourceTokenUpdateRowData {
					row := valid
					row.CreatedAt = pgtype.Timestamptz{}
					return row
				}()),
			},
			validate: func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
				t.Fatal("unexpected validate")
				return nil
			},
			wantText:    "map management external integration source token update before token",
			wantQueries: 2,
		},
		{
			name: "after mapping",
			rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateTokenRow(valid),
				managementExternalIntegrationSourceTokenUpdateTokenRow(func() managementExternalIntegrationSourceTokenUpdateRowData {
					row := valid
					row.UpdatedAt = pgtype.Timestamptz{}
					return row
				}()),
			},
			validate: func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
				t.Fatal("unexpected validate")
				return nil
			},
			wantText:    "map management external integration source token update after token",
			wantQueries: 3,
		},
		{
			name: "validate",
			rows: []pgx.Row{
				managementExternalIntegrationSourceTokenUpdateRow("source_1"),
				managementExternalIntegrationSourceTokenUpdateTokenRow(valid),
				managementExternalIntegrationSourceTokenUpdateTokenRow(valid),
			},
			validate:      func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { return validationErr },
			wantErr:       validationErr,
			wantQueries:   3,
			wantValidates: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), managementExternalIntegrationSourceTokenUpdateContextKey{}, "rollback-value")
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			tx := &managementExternalIntegrationSourceTokenUpdateTxStub{rows: test.rows}
			validateCalls := 0
			_, err := updateManagementExternalIntegrationSourceTokenInTx(
				ctx,
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
				port.ManagementExternalIntegrationSourceTokenUpdateInput{SourceID: "source_1", TokenID: "token_1", UpdatedAt: now},
				func(result port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
					validateCalls++
					return test.validate(result)
				},
			)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want context %q", err, test.wantText)
			}
			if len(tx.querySQL) != test.wantQueries || validateCalls != test.wantValidates || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("queries/validate/commit/rollback = %d/%d/%d/%d", len(tx.querySQL), validateCalls, tx.commitCalls, tx.rollbackCalls)
			}
			managementExternalIntegrationSourceTokenUpdateAssertIndependentRollbackContext(t, tx)
		})
	}
}

func TestUpdateManagementExternalIntegrationSourceTokenCommitFailureRollsBackWithIndependentContext(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	row := managementExternalIntegrationSourceTokenUpdateTestRow(
		"source_1", "token_1", "Token", "active", `[]`, nil, nil,
		now.Add(-time.Hour), now.Add(-time.Minute), nil,
	)
	commitErr := errors.New("commit unavailable")
	tx := &managementExternalIntegrationSourceTokenUpdateTxStub{
		rows: []pgx.Row{
			managementExternalIntegrationSourceTokenUpdateRow("source_1"),
			managementExternalIntegrationSourceTokenUpdateTokenRow(row),
			managementExternalIntegrationSourceTokenUpdateTokenRow(row),
		},
		commitErr: commitErr,
	}
	ctx := context.WithValue(context.Background(), managementExternalIntegrationSourceTokenUpdateContextKey{}, "rollback-value")
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	validateCalls := 0
	_, err := updateManagementExternalIntegrationSourceTokenInTx(
		ctx,
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ManagementExternalIntegrationSourceTokenUpdateInput{SourceID: "source_1", TokenID: "token_1", UpdatedAt: now},
		func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error { validateCalls++; return nil },
	)
	if !errors.Is(err, commitErr) || !strings.Contains(err.Error(), "commit management external integration source token update") {
		t.Fatalf("commit error = %v", err)
	}
	if validateCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("validate/commit/rollback = %d/%d/%d", validateCalls, tx.commitCalls, tx.rollbackCalls)
	}
	managementExternalIntegrationSourceTokenUpdateAssertIndependentRollbackContext(t, tx)
}

type managementExternalIntegrationSourceTokenUpdateRowData struct {
	SourceRefID string
	ID          string
	Name        string
	TokenPrefix string
	TokenSuffix string
	Status      string
	ScopesJSON  string
	ExpiresAt   pgtype.Timestamptz
	LastUsedAt  pgtype.Timestamptz
	CreatedAt   pgtype.Timestamptz
	UpdatedAt   pgtype.Timestamptz
	RevokedAt   pgtype.Timestamptz
}

func managementExternalIntegrationSourceTokenUpdateTestRow(
	sourceID string,
	tokenID string,
	name string,
	status string,
	scopesJSON string,
	expiresAt *time.Time,
	lastUsedAt *time.Time,
	createdAt time.Time,
	updatedAt time.Time,
	revokedAt *time.Time,
) managementExternalIntegrationSourceTokenUpdateRowData {
	return managementExternalIntegrationSourceTokenUpdateRowData{
		SourceRefID: sourceID,
		ID:          tokenID,
		Name:        name,
		TokenPrefix: "juis_prefix",
		TokenSuffix: "12345678",
		Status:      status,
		ScopesJSON:  scopesJSON,
		ExpiresAt:   pgTimestamptzPtr(expiresAt),
		LastUsedAt:  pgTimestamptzPtr(lastUsedAt),
		CreatedAt:   pgTimestamptz(createdAt),
		UpdatedAt:   pgTimestamptz(updatedAt),
		RevokedAt:   pgTimestamptzPtr(revokedAt),
	}
}

type managementExternalIntegrationSourceTokenUpdateTxStub struct {
	pgx.Tx
	rows                 []pgx.Row
	querySQL             []string
	updateArgs           []any
	calls                []string
	commitErr            error
	commitCalls          int
	rollbackCalls        int
	rollbackContextErr   error
	rollbackContextValue any
	rollbackHasDeadline  bool
	rollbackDeadline     time.Time
}

func (s *managementExternalIntegrationSourceTokenUpdateTxStub) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	s.querySQL = append(s.querySQL, sql)
	switch sql {
	case managementExternalIntegrationSourceTokenUpdateSourceLockSQL:
		s.calls = append(s.calls, "source-lock")
	case managementExternalIntegrationSourceTokenUpdateTokenLockSQL:
		s.calls = append(s.calls, "token-lock")
	case managementExternalIntegrationSourceTokenUpdateSQL:
		s.calls = append(s.calls, "update")
		s.updateArgs = append([]any(nil), args...)
	default:
		return managementExternalIntegrationSourceTokenUpdateErrorRow(fmt.Errorf("unexpected SQL: %s", sql))
	}
	if len(s.rows) == 0 {
		return managementExternalIntegrationSourceTokenUpdateErrorRow(errors.New("missing stub row"))
	}
	row := s.rows[0]
	s.rows = s.rows[1:]
	return row
}

func (s *managementExternalIntegrationSourceTokenUpdateTxStub) Commit(context.Context) error {
	s.calls = append(s.calls, "commit")
	s.commitCalls++
	return s.commitErr
}

func (s *managementExternalIntegrationSourceTokenUpdateTxStub) Rollback(ctx context.Context) error {
	s.rollbackCalls++
	s.rollbackContextErr = ctx.Err()
	s.rollbackContextValue = ctx.Value(managementExternalIntegrationSourceTokenUpdateContextKey{})
	s.rollbackDeadline, s.rollbackHasDeadline = ctx.Deadline()
	return nil
}

type managementExternalIntegrationSourceTokenUpdateStaticRow struct {
	values []any
	err    error
}

func (r managementExternalIntegrationSourceTokenUpdateStaticRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destinations = %d, values = %d", len(dest), len(r.values))
	}
	for index := range dest {
		target := reflect.ValueOf(dest[index])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return fmt.Errorf("scan destination %d is not a pointer", index)
		}
		target.Elem().Set(reflect.ValueOf(r.values[index]))
	}
	return nil
}

func managementExternalIntegrationSourceTokenUpdateRow(sourceID string) pgx.Row {
	return managementExternalIntegrationSourceTokenUpdateStaticRow{values: []any{sourceID}}
}

func managementExternalIntegrationSourceTokenUpdateTokenRow(row managementExternalIntegrationSourceTokenUpdateRowData) pgx.Row {
	return managementExternalIntegrationSourceTokenUpdateStaticRow{values: []any{
		row.SourceRefID,
		row.ID,
		row.Name,
		row.TokenPrefix,
		row.TokenSuffix,
		row.Status,
		row.ScopesJSON,
		row.ExpiresAt,
		row.LastUsedAt,
		row.CreatedAt,
		row.UpdatedAt,
		row.RevokedAt,
	}}
}

func managementExternalIntegrationSourceTokenUpdateErrorRow(err error) pgx.Row {
	return managementExternalIntegrationSourceTokenUpdateStaticRow{err: err}
}

func optionalTimeEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func managementExternalIntegrationSourceTokenUpdateAssertScopesJSONArray(
	t *testing.T,
	raw string,
	want []string,
) {
	t.Helper()
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal scopes JSON array %q: %v", raw, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes JSON array = %#v, want %#v", got, want)
	}
}

type managementExternalIntegrationSourceTokenUpdateContextKey struct{}

func managementExternalIntegrationSourceTokenUpdateAssertIndependentRollbackContext(
	t *testing.T,
	tx *managementExternalIntegrationSourceTokenUpdateTxStub,
) {
	t.Helper()
	if tx.rollbackContextErr != nil || tx.rollbackContextValue != "rollback-value" || !tx.rollbackHasDeadline {
		t.Fatalf(
			"rollback context err/value/deadline = %v/%#v/%t",
			tx.rollbackContextErr,
			tx.rollbackContextValue,
			tx.rollbackHasDeadline,
		)
	}
	remaining := time.Until(tx.rollbackDeadline)
	if remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("rollback deadline remaining = %s, want within (0, 5s]", remaining)
	}
}

var _ pgx.Tx = (*managementExternalIntegrationSourceTokenUpdateTxStub)(nil)
var _ pgx.Row = managementExternalIntegrationSourceTokenUpdateStaticRow{}
