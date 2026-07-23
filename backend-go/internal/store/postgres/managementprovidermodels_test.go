package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementProviderModelReadQueriesStayPointAndWindowBounded(t *testing.T) {
	source, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model query: %v", err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")
	enabledCodesStart := strings.Index(sql, "-- name: ListManagementEnabledModelProviderCodes :many")
	protocolCodesStart := strings.Index(sql, "-- name: ListManagementProviderCodesByProtocol :many")
	catalogStart := strings.Index(sql, "-- name: ListManagementProviderModelCatalog :many")
	optionsStart := strings.Index(sql, "-- name: ListManagementProviderModelOptions :many")
	capabilitiesStart := strings.Index(sql, "-- name: ListManagementProviderModelCapabilityCandidates :many")
	if enabledCodesStart < 0 || protocolCodesStart <= enabledCodesStart || catalogStart <= protocolCodesStart || optionsStart < 0 || capabilitiesStart <= optionsStart {
		t.Fatalf("provider model SQL missing options/capabilities queries")
	}
	for name, query := range map[string]string{
		"enabled provider codes":  sql[enabledCodesStart:protocolCodesStart],
		"protocol provider codes": sql[protocolCodesStart:catalogStart],
	} {
		if strings.Contains(query, "LIMIT ") {
			t.Fatalf("%s SQL must not truncate an unpaginated source set: %s", name, query)
		}
	}
	optionsSQL := sql[optionsStart:capabilitiesStart]
	capabilitiesSQL := sql[capabilitiesStart:]
	for _, want := range []string{
		"ROW_NUMBER() OVER",
		"catalog_visible = true",
		"model = ANY(sqlc.arg(selected_ids)::text[])",
		"lower(built_in.model) LIKE",
		"LIMIT sqlc.arg(result_limit)",
		"custom.scope = 'personal'",
	} {
		if !strings.Contains(optionsSQL, want) {
			t.Fatalf("provider model options SQL missing %q", want)
		}
	}
	for _, forbidden := range []string{"input_usd_per_1m", "supported_service_tiers_json", "capability_notes"} {
		if strings.Contains(optionsSQL, forbidden) {
			t.Fatalf("provider model options SQL must stay lightweight, found %q", forbidden)
		}
	}
	for _, want := range []string{
		"built_in.model = sqlc.arg(model)",
		"built_in.status = 'active'",
		"built_in.catalog_visible = true",
		"custom.model = sqlc.arg(model)",
		"custom.scope = 'personal'",
		"supported_reasoning_efforts_json",
	} {
		if !strings.Contains(capabilitiesSQL, want) {
			t.Fatalf("provider model capabilities SQL missing %q", want)
		}
	}
}

func TestMarshalManagementProviderModelPriceMapNormalizesNilToObject(t *testing.T) {
	encoded, err := marshalManagementProviderModelPriceMap(nil)
	if err != nil {
		t.Fatalf("marshalManagementProviderModelPriceMap(nil) error = %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("marshalManagementProviderModelPriceMap(nil) = %s, want {}", encoded)
	}

	price := 1.25
	encoded, err = marshalManagementProviderModelPriceMap(map[string]port.ManagementProviderModelPriceSet{
		"priority": {InputUSDPer1M: &price},
	})
	if err != nil {
		t.Fatalf("marshalManagementProviderModelPriceMap(non-nil) error = %v", err)
	}
	if string(encoded) != `{"priority":{"inputUsdPer1M":1.25}}` {
		t.Fatalf("marshalManagementProviderModelPriceMap(non-nil) = %s", encoded)
	}
}

func TestMarkManagementModelCatalogSnapshotDirtyMapsScope(t *testing.T) {
	q := &managementCustomProviderModelUpdateQueriesStub{}
	if err := markManagementModelCatalogSnapshotDirty(t.Context(), q, "personal", " sys_user "); err != nil {
		t.Fatalf("mark personal dirty: %v", err)
	}
	if q.dirtyInput.Scope != "personal" || q.dirtyInput.SystemAccountID != "sys_user" {
		t.Fatalf("personal dirty input = %+v", q.dirtyInput)
	}
	if err := markManagementModelCatalogSnapshotDirty(t.Context(), q, "global", "sys_user"); err != nil {
		t.Fatalf("mark global dirty: %v", err)
	}
	if q.dirtyInput.Scope != "all" || q.dirtyInput.SystemAccountID != "" {
		t.Fatalf("global dirty input = %+v", q.dirtyInput)
	}
}

func TestUpdateManagementCustomProviderModelMergesLockedSnapshotAndValidatesBeforeUpdate(t *testing.T) {
	price := 1.0
	newPrice := 2.0
	locked := customProviderModelRow("custom_1", "gpt", "chat", "personal", "sys_user", "active", "text", `[]`, `[]`, `[]`, "", "", "", &price)
	updated := locked
	updated.InputUsdPer1m = pgtype.Float8{Float64: newPrice, Valid: true}
	q := &managementCustomProviderModelUpdateQueriesStub{locked: locked, updated: customProviderModelUpdateRowFromLock(updated)}
	input := port.ManagementCustomProviderModelUpdateInput{ID: "custom_1", ProviderCode: "gpt", ActorSystemAccountID: "sys_user", InputUSDPer1M: port.ManagementProviderModelOptionalFloat{Present: true, Value: &newPrice}, SupportedAPIProtocols: port.ManagementProviderModelOptionalStringList{Present: true, Value: []string{}}}
	validateCalls := 0
	result, found, err := updateManagementCustomProviderModelTx(context.Background(), q, input, func(got port.ManagementCustomProviderModelUpdateResult) error {
		validateCalls++
		if got.Before.InputUSDPer1M == nil || *got.Before.InputUSDPer1M != price || got.After.InputUSDPer1M == nil || *got.After.InputUSDPer1M != newPrice || got.After.SupportedAPIProtocols == nil {
			t.Fatalf("snapshots = %+v", got)
		}
		return nil
	})
	if err != nil || !found || validateCalls != 1 || q.updateCalls != 1 {
		t.Fatalf("result=%+v found=%t err=%v validate=%d update=%d", result, found, err, validateCalls, q.updateCalls)
	}
	if q.updateInput.InputUsdPer1m.Float64 != newPrice || !q.updateInput.InputUsdPer1m.Valid || q.updateInput.SupportedApiProtocolsJson != "[]" {
		t.Fatalf("update input = %+v", q.updateInput)
	}
	if q.dirtyInput.Scope != "personal" || q.dirtyInput.SystemAccountID != "sys_user" || !q.dirtyInput.UpdatedAt.Valid {
		t.Fatalf("dirty input = %+v", q.dirtyInput)
	}
}

func TestUpdateManagementCustomProviderModelValidationFailureSkipsUpdate(t *testing.T) {
	locked := customProviderModelRow("custom_1", "gpt", "chat", "personal", "sys_user", "active", "text", `[]`, `[]`, `[]`, "", "", "", nil)
	q := &managementCustomProviderModelUpdateQueriesStub{locked: locked}
	validateErr := errors.New("invalid candidate")
	_, found, err := updateManagementCustomProviderModelTx(context.Background(), q, port.ManagementCustomProviderModelUpdateInput{ID: "custom_1", ProviderCode: "gpt"}, func(port.ManagementCustomProviderModelUpdateResult) error { return validateErr })
	if !errors.Is(err, validateErr) || found || q.updateCalls != 0 {
		t.Fatalf("err=%v found=%t update=%d", err, found, q.updateCalls)
	}
}

func TestUpdateManagementCustomProviderModelNotFoundAndIdentityErrors(t *testing.T) {
	q := &managementCustomProviderModelUpdateQueriesStub{lockErr: pgx.ErrNoRows}
	_, found, err := updateManagementCustomProviderModelTx(context.Background(), q, port.ManagementCustomProviderModelUpdateInput{ID: "missing", ProviderCode: "gpt"}, func(port.ManagementCustomProviderModelUpdateResult) error { return nil })
	if err != nil || found {
		t.Fatalf("not found err=%v found=%t", err, found)
	}
	locked := customProviderModelRow("custom_1", "gpt", "chat", "personal", "sys_user", "active", "text", `[]`, `[]`, `[]`, "", "", "", nil)
	updated := locked
	updated.ID = "other"
	q = &managementCustomProviderModelUpdateQueriesStub{locked: locked, updated: customProviderModelUpdateRowFromLock(updated)}
	_, found, err = updateManagementCustomProviderModelTx(context.Background(), q, port.ManagementCustomProviderModelUpdateInput{ID: "custom_1", ProviderCode: "gpt"}, func(port.ManagementCustomProviderModelUpdateResult) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "identity") || found {
		t.Fatalf("identity err=%v found=%t", err, found)
	}
}

func TestUpdateManagementCustomProviderModelTransactionPropagatesErrorsAndRollsBack(t *testing.T) {
	operationErr := errors.New("update failed")
	locked := customProviderModelRow("custom_1", "gpt", "chat", "personal", "sys_user", "active", "text", `[]`, `[]`, `[]`, "", "", "", nil)
	tests := []struct {
		name      string
		updateErr error
		commitErr error
		wantErr   error
		wantCalls []string
	}{
		{name: "update", updateErr: operationErr, wantErr: operationErr, wantCalls: []string{"lock", "update", "rollback"}},
		{name: "commit", commitErr: operationErr, wantErr: operationErr, wantCalls: []string{"lock", "update", "mark-dirty", "commit", "rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &managementCustomProviderModelUpdateTxStub{locked: locked, updated: customProviderModelUpdateRowFromLock(locked), updateErr: tt.updateErr, commitErr: tt.commitErr}
			_, found, err := updateManagementCustomProviderModelInTx(context.Background(), func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil }, port.ManagementCustomProviderModelUpdateInput{ID: "custom_1", ProviderCode: "gpt"}, func(port.ManagementCustomProviderModelUpdateResult) error { return nil })
			if !errors.Is(err, tt.wantErr) || (tt.name == "update" && found) || (tt.name == "commit" && found) || !reflect.DeepEqual(tx.calls, tt.wantCalls) {
				t.Fatalf("err=%v found=%t calls=%v", err, found, tx.calls)
			}
		})
	}
}

type managementCustomProviderModelUpdateQueriesStub struct {
	locked      postgresqueries.LockManagementCustomProviderModelRow
	lockErr     error
	updated     postgresqueries.UpdateManagementCustomProviderModelRow
	updateErr   error
	updateCalls int
	updateInput postgresqueries.UpdateManagementCustomProviderModelParams
	dirtyInput  postgresqueries.MarkManagementModelCatalogSnapshotRebuildDirtyParams
	dirtyErr    error
}

func (s *managementCustomProviderModelUpdateQueriesStub) LockManagementCustomProviderModel(context.Context, postgresqueries.LockManagementCustomProviderModelParams) (postgresqueries.LockManagementCustomProviderModelRow, error) {
	return s.locked, s.lockErr
}
func (s *managementCustomProviderModelUpdateQueriesStub) UpdateManagementCustomProviderModel(_ context.Context, input postgresqueries.UpdateManagementCustomProviderModelParams) (postgresqueries.UpdateManagementCustomProviderModelRow, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updated, s.updateErr
}
func (s *managementCustomProviderModelUpdateQueriesStub) MarkManagementModelCatalogSnapshotRebuildDirty(_ context.Context, input postgresqueries.MarkManagementModelCatalogSnapshotRebuildDirtyParams) error {
	s.dirtyInput = input
	return s.dirtyErr
}

type managementCustomProviderModelUpdateTxStub struct {
	pgx.Tx
	locked               postgresqueries.LockManagementCustomProviderModelRow
	updated              postgresqueries.UpdateManagementCustomProviderModelRow
	updateErr, commitErr error
	calls                []string
}

func (s *managementCustomProviderModelUpdateTxStub) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if strings.Contains(sql, "FOR UPDATE") {
		s.calls = append(s.calls, "lock")
		return managementProviderModelStaticRow{values: customProviderModelValues(s.locked)}
	}
	if strings.Contains(sql, "UPDATE juhe_business.custom_provider_models") {
		s.calls = append(s.calls, "update")
		if s.updateErr != nil {
			return managementProviderModelStaticRow{err: s.updateErr}
		}
		return managementProviderModelStaticRow{values: customProviderModelValues(s.updated)}
	}
	return managementProviderModelStaticRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
}
func (s *managementCustomProviderModelUpdateTxStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "model_catalog_snapshot_rebuild_requests") {
		s.calls = append(s.calls, "mark-dirty")
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("unexpected SQL: %s", sql)
}
func (s *managementCustomProviderModelUpdateTxStub) Commit(context.Context) error {
	s.calls = append(s.calls, "commit")
	return s.commitErr
}
func (s *managementCustomProviderModelUpdateTxStub) Rollback(context.Context) error {
	s.calls = append(s.calls, "rollback")
	return nil
}

func customProviderModelRow(id, provider, model, scope, owner, status, mode, protocols, tiers, reasoning, defaultReasoning, release, shutdown string, price *float64) postgresqueries.LockManagementCustomProviderModelRow {
	row := postgresqueries.LockManagementCustomProviderModelRow{ID: id, ProviderCode: provider, Model: model, Scope: scope, SystemAccountID: pgtype.Text{String: owner, Valid: owner != ""}, Status: status, CatalogVisible: true, Mode: pgtype.Text{String: mode, Valid: mode != ""}, SupportedApiProtocolsJson: protocols, SupportedServiceTiersJson: tiers, SupportedReasoningEffortsJson: reasoning, DefaultReasoningEffort: pgtype.Text{String: defaultReasoning, Valid: defaultReasoning != ""}, ReleaseDate: pgtype.Text{String: release, Valid: release != ""}, ShutdownDate: pgtype.Text{String: shutdown, Valid: shutdown != ""}, ServiceTierPricesJson: `{}`, CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	if price != nil {
		row.InputUsdPer1m = pgtype.Float8{Float64: *price, Valid: true}
	}
	return row
}

func customProviderModelUpdateRowFromLock(row postgresqueries.LockManagementCustomProviderModelRow) postgresqueries.UpdateManagementCustomProviderModelRow {
	return postgresqueries.UpdateManagementCustomProviderModelRow{ID: row.ID, ProviderCode: row.ProviderCode, Model: row.Model, Scope: row.Scope, SystemAccountID: row.SystemAccountID, Status: row.Status, CatalogVisible: row.CatalogVisible, Mode: row.Mode, SupportedApiProtocolsJson: row.SupportedApiProtocolsJson, SupportedServiceTiersJson: row.SupportedServiceTiersJson, SupportedReasoningEffortsJson: row.SupportedReasoningEffortsJson, DefaultReasoningEffort: row.DefaultReasoningEffort, ReleaseDate: row.ReleaseDate, ShutdownDate: row.ShutdownDate, ContextWindowTokens: row.ContextWindowTokens, MaxInputTokens: row.MaxInputTokens, MaxOutputTokens: row.MaxOutputTokens, InputUsdPer1m: row.InputUsdPer1m, OutputUsdPer1m: row.OutputUsdPer1m, CachedInputUsdPer1m: row.CachedInputUsdPer1m, CacheWriteUsdPer1m: row.CacheWriteUsdPer1m, CacheWrite1hUsdPer1m: row.CacheWrite1hUsdPer1m, ServiceTierPricesJson: row.ServiceTierPricesJson, ImageInputUsdPer1m: row.ImageInputUsdPer1m, ImageOutputUsdPer1m: row.ImageOutputUsdPer1m, AudioInputUsdPer1m: row.AudioInputUsdPer1m, AudioOutputUsdPer1m: row.AudioOutputUsdPer1m, OutputUsdPerImage: row.OutputUsdPerImage, PricingNotes: row.PricingNotes, CapabilityNotes: row.CapabilityNotes, Notes: row.Notes, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}
func customProviderModelValues(row interface{}) []any {
	switch value := row.(type) {
	case postgresqueries.LockManagementCustomProviderModelRow:
		return []any{value.ID, value.ProviderCode, value.Model, value.Scope, value.SystemAccountID, value.Status, value.CatalogVisible, value.Mode, value.SupportedApiProtocolsJson, value.SupportedServiceTiersJson, value.SupportedReasoningEffortsJson, value.DefaultReasoningEffort, value.ReleaseDate, value.ShutdownDate, value.ContextWindowTokens, value.MaxInputTokens, value.MaxOutputTokens, value.InputUsdPer1m, value.OutputUsdPer1m, value.CachedInputUsdPer1m, value.CacheWriteUsdPer1m, value.CacheWrite1hUsdPer1m, value.ServiceTierPricesJson, value.ImageInputUsdPer1m, value.ImageOutputUsdPer1m, value.AudioInputUsdPer1m, value.AudioOutputUsdPer1m, value.OutputUsdPerImage, value.PricingNotes, value.CapabilityNotes, value.Notes, value.CreatedBy, value.UpdatedBy, value.CreatedAt, value.UpdatedAt}
	case postgresqueries.UpdateManagementCustomProviderModelRow:
		return []any{value.ID, value.ProviderCode, value.Model, value.Scope, value.SystemAccountID, value.Status, value.CatalogVisible, value.Mode, value.SupportedApiProtocolsJson, value.SupportedServiceTiersJson, value.SupportedReasoningEffortsJson, value.DefaultReasoningEffort, value.ReleaseDate, value.ShutdownDate, value.ContextWindowTokens, value.MaxInputTokens, value.MaxOutputTokens, value.InputUsdPer1m, value.OutputUsdPer1m, value.CachedInputUsdPer1m, value.CacheWriteUsdPer1m, value.CacheWrite1hUsdPer1m, value.ServiceTierPricesJson, value.ImageInputUsdPer1m, value.ImageOutputUsdPer1m, value.AudioInputUsdPer1m, value.AudioOutputUsdPer1m, value.OutputUsdPerImage, value.PricingNotes, value.CapabilityNotes, value.Notes, value.CreatedBy, value.UpdatedBy, value.CreatedAt, value.UpdatedAt}
	default:
		return nil
	}
}
