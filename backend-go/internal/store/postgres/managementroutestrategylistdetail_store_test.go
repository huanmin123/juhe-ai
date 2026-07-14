package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestListManagementRouteStrategiesSelectsScopeAndKeywordQuery(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 1, 2, 3, 0, time.FixedZone("UTC+8", 8*60*60))
	updatedAt := createdAt.Add(time.Hour)
	record := managementRouteStrategyListRecord{
		ID:                "route_1",
		SystemAccountID:   "sys_owner",
		SystemAccountName: "所有者",
		Name:              "route%literal",
		Description:       pgtype.Text{String: "说明", Valid: true},
		Mode:              "normal",
		Status:            "active",
		IsDefault:         true,
		ConfigJSON:        pgtype.Text{String: `{"normalRoutingConfig":{"schedulingPreference":"cost_first"}}`, Valid: true},
		CreatedAt:         pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:         pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}
	tests := []struct {
		name      string
		input     port.ManagementRouteStrategyListInput
		wantQuery string
	}{
		{
			name:      "global",
			input:     port.ManagementRouteStrategyListInput{Limit: 2, Offset: 2000},
			wantQuery: "global",
		},
		{
			name:      "owner",
			input:     port.ManagementRouteStrategyListInput{SystemAccountID: " sys_owner ", Limit: 2, Offset: 2000},
			wantQuery: "owner",
		},
		{
			name:      "owner preserves U+0085",
			input:     port.ManagementRouteStrategyListInput{SystemAccountID: "\u0085", Limit: 2, Offset: 2000},
			wantQuery: "owner",
		},
		{
			name:      "global keyword",
			input:     port.ManagementRouteStrategyListInput{Keyword: " route% ", Limit: 2, Offset: 2000},
			wantQuery: "global_keyword",
		},
		{
			name: "owner keyword",
			input: port.ManagementRouteStrategyListInput{
				SystemAccountID: " sys_owner ",
				Keyword:         " route_ ",
				Mode:            " normal ",
				Status:          " active ",
				Limit:           2,
				Offset:          2000,
			},
			wantQuery: "owner_keyword",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &managementRouteStrategyListDetailQueriesStub{listRecord: record}
			page, err := listManagementRouteStrategies(context.Background(), q, tt.input)
			if err != nil {
				t.Fatalf("listManagementRouteStrategies() error = %v", err)
			}
			if !reflect.DeepEqual(q.listCalls, []string{tt.wantQuery}) {
				t.Fatalf("query calls = %#v, want %q", q.listCalls, tt.wantQuery)
			}
			if len(page.Rows) != 2 || !page.HasMore {
				t.Fatalf("page = %#v, want two untrimmed lookahead rows with hasMore", page)
			}
			row := page.Rows[0]
			if row.ID != record.ID || row.SystemAccountID != record.SystemAccountID ||
				row.Description == nil || *row.Description != "说明" ||
				row.ConfigJSON == nil || *row.ConfigJSON == "" {
				t.Fatalf("mapped row = %#v", row)
			}
			if !row.CreatedAt.Equal(createdAt.UTC()) || !row.UpdatedAt.Equal(updatedAt.UTC()) {
				t.Fatalf("row timestamps = %v/%v", row.CreatedAt, row.UpdatedAt)
			}
			if q.lastOffset != 2000 || q.lastLimit != 2 {
				t.Fatalf("pagination = offset %d limit %d", q.lastOffset, q.lastLimit)
			}
			if tt.wantQuery == "owner" || tt.wantQuery == "owner_keyword" {
				if q.lastSystemAccountID != tt.input.SystemAccountID {
					t.Fatalf("owner = %q", q.lastSystemAccountID)
				}
			}
			if tt.wantQuery == "owner_keyword" {
				if q.lastKeyword != "route_" || q.lastKeywordUpper != textPrefixUpperBound("route_") {
					t.Fatalf("keyword bounds = %q/%q", q.lastKeyword, q.lastKeywordUpper)
				}
				if q.lastMode != "normal" || q.lastStatus != "active" {
					t.Fatalf("filters = %q/%q", q.lastMode, q.lastStatus)
				}
			}
		})
	}
}

func TestListManagementRouteStrategyListEnrichmentUsesAlignedScopesAndAggregatesPreview(t *testing.T) {
	q := &managementRouteStrategyListDetailQueriesStub{
		enrichmentRows: []postgresqueries.ListManagementRouteStrategyListEnrichmentRow{
			{
				RouteStrategyID: "route_same",
				SystemAccountID: "sys_a",
				BindingCount:    4,
				ApiKeyCount:     2,
				BindingID:       pgtype.Text{String: "binding_a1", Valid: true},
				GroupID:         pgtype.Text{String: "group_a1", Valid: true},
				GroupName:       pgtype.Text{String: "A1", Valid: true},
				ProviderCode:    pgtype.Text{String: "openai", Valid: true},
				Priority:        pgtype.Int4{Int32: 1, Valid: true},
				Weight:          pgtype.Int4{Int32: 80, Valid: true},
				BindingStatus:   pgtype.Text{String: "active", Valid: true},
				GroupEnabled:    pgtype.Bool{Bool: true, Valid: true},
			},
			{
				RouteStrategyID: "route_same",
				SystemAccountID: "sys_a",
				BindingCount:    4,
				ApiKeyCount:     2,
				BindingID:       pgtype.Text{String: "binding_a2", Valid: true},
				GroupID:         pgtype.Text{String: "group_a2", Valid: true},
				Priority:        pgtype.Int4{Int32: 2, Valid: true},
				Weight:          pgtype.Int4{Int32: 20, Valid: true},
				BindingStatus:   pgtype.Text{String: "disabled", Valid: true},
				GroupEnabled:    pgtype.Bool{Bool: false, Valid: true},
			},
			{
				RouteStrategyID: "route_same",
				SystemAccountID: "sys_b",
				BindingCount:    0,
				ApiKeyCount:     7,
			},
		},
	}
	rows, err := listManagementRouteStrategyListEnrichment(context.Background(), q, []port.ManagementRouteStrategyScope{
		{ID: "route_same", SystemAccountID: "sys_a"},
		{ID: "route_same", SystemAccountID: "sys_a"},
		{ID: "route_same", SystemAccountID: "sys_b"},
		{ID: "", SystemAccountID: "sys_ignored"},
	})
	if err != nil {
		t.Fatalf("listManagementRouteStrategyListEnrichment() error = %v", err)
	}
	if !reflect.DeepEqual(q.enrichmentParams.RouteStrategyIds, []string{"route_same", "route_same"}) ||
		!reflect.DeepEqual(q.enrichmentParams.SystemAccountIds, []string{"sys_a", "sys_b"}) {
		t.Fatalf("enrichment params = %#v", q.enrichmentParams)
	}
	if len(rows) != 2 {
		t.Fatalf("enrichment rows = %#v", rows)
	}
	if rows[0].ID != "route_same" || rows[0].SystemAccountID != "sys_a" ||
		rows[0].BindingCount != 4 || rows[0].APIKeyCount != 2 ||
		len(rows[0].GroupBindingPreview) != 2 {
		t.Fatalf("first enrichment = %#v", rows[0])
	}
	if rows[0].GroupBindingPreview[0].Weight != 80 || !rows[0].GroupBindingPreview[0].GroupEnabled {
		t.Fatalf("first preview = %#v", rows[0].GroupBindingPreview[0])
	}
	if rows[1].SystemAccountID != "sys_b" || rows[1].BindingCount != 0 ||
		rows[1].APIKeyCount != 7 || len(rows[1].GroupBindingPreview) != 0 {
		t.Fatalf("second enrichment = %#v", rows[1])
	}
}

func TestListManagementRouteStrategyListEnrichmentPreservesScopeValues(t *testing.T) {
	q := &managementRouteStrategyListDetailQueriesStub{}
	rows, err := listManagementRouteStrategyListEnrichment(context.Background(), q, []port.ManagementRouteStrategyScope{
		{ID: " route_same ", SystemAccountID: "\u0085"},
		{ID: "route_same", SystemAccountID: "\u0085"},
		{ID: "", SystemAccountID: "\u0085"},
		{ID: "route_ignored", SystemAccountID: ""},
	})
	if err != nil {
		t.Fatalf("listManagementRouteStrategyListEnrichment() error = %v", err)
	}
	if !reflect.DeepEqual(q.enrichmentParams.RouteStrategyIds, []string{" route_same ", "route_same"}) ||
		!reflect.DeepEqual(q.enrichmentParams.SystemAccountIds, []string{"\u0085", "\u0085"}) {
		t.Fatalf("enrichment params = %#v", q.enrichmentParams)
	}
	if len(rows) != 2 || rows[0].ID != " route_same " || rows[0].SystemAccountID != "\u0085" {
		t.Fatalf("enrichment rows = %#v", rows)
	}
}

func TestFindManagementRouteStrategyDetailMapsBindingsAndRejectsTwentyFirst(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	q := &managementRouteStrategyListDetailQueriesStub{
		detailRow: postgresqueries.FindManagementRouteStrategyDetailRow{
			ID:                "route_1",
			SystemAccountID:   "sys_owner",
			SystemAccountName: "所有者",
			Name:              "策略",
			Description:       pgtype.Text{String: "说明", Valid: true},
			Mode:              "weighted",
			Status:            "active",
			ConfigJson:        pgtype.Text{String: `{}`, Valid: true},
			ApiKeyCount:       9,
			CreatedAt:         pgtype.Timestamptz{Time: createdAt, Valid: true},
			UpdatedAt:         pgtype.Timestamptz{Time: updatedAt, Valid: true},
		},
		detailBindings: []postgresqueries.ListManagementRouteStrategyDetailBindingsRow{
			{
				ID:           "binding_1",
				GroupID:      "group_1",
				GroupName:    pgtype.Text{String: "分组", Valid: true},
				ProviderCode: pgtype.Text{String: "openai", Valid: true},
				Priority:     1,
				Weight:       100,
				Status:       "active",
				GroupEnabled: true,
			},
		},
	}
	row, found, err := findManagementRouteStrategyDetail(context.Background(), q, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: "route_1",
		SystemAccountID: "sys_owner",
	})
	if err != nil {
		t.Fatalf("findManagementRouteStrategyDetail() error = %v", err)
	}
	if !found || row.ID != "route_1" || row.APIKeyCount != 9 || len(row.GroupBindings) != 1 {
		t.Fatalf("detail = %#v, found = %v", row, found)
	}
	if row.GroupBindings[0].Priority != 1 || row.GroupBindings[0].Weight != 100 ||
		!row.GroupBindings[0].GroupEnabled {
		t.Fatalf("binding = %#v", row.GroupBindings[0])
	}

	q.detailBindings = make([]postgresqueries.ListManagementRouteStrategyDetailBindingsRow, 21)
	for index := range q.detailBindings {
		q.detailBindings[index] = postgresqueries.ListManagementRouteStrategyDetailBindingsRow{
			ID:       "binding",
			GroupID:  "group",
			Priority: int32(index + 1),
			Weight:   1,
			Status:   "active",
		}
	}
	_, _, err = findManagementRouteStrategyDetail(context.Background(), q, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: "route_1",
		SystemAccountID: "sys_owner",
	})
	if err == nil || !strings.Contains(err.Error(), "超过 20") {
		t.Fatalf("twenty-first binding error = %v", err)
	}
}

func TestFindManagementRouteStrategyDetailHandlesNotFoundAndQueryErrors(t *testing.T) {
	q := &managementRouteStrategyListDetailQueriesStub{detailErr: pgx.ErrNoRows}
	_, found, err := findManagementRouteStrategyDetail(context.Background(), q, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: "missing",
	})
	if err != nil || found || len(q.detailBindingParams) != 0 {
		t.Fatalf("not found = %v, err = %v, binding calls = %#v", found, err, q.detailBindingParams)
	}

	q.detailErr = errors.New("detail failed")
	if _, _, err := findManagementRouteStrategyDetail(context.Background(), q, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: "route_1",
	}); err == nil || !strings.Contains(err.Error(), "find management route strategy detail") {
		t.Fatalf("detail error = %v", err)
	}

	q.detailErr = nil
	q.detailRow = postgresqueries.FindManagementRouteStrategyDetailRow{
		ID:                "route_1",
		SystemAccountID:   "sys_owner",
		SystemAccountName: "所有者",
		Name:              "策略",
		Mode:              "normal",
		Status:            "active",
		CreatedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
		UpdatedAt:         pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	q.detailBindingErr = errors.New("bindings failed")
	if _, _, err := findManagementRouteStrategyDetail(context.Background(), q, port.ManagementRouteStrategyDetailInput{
		RouteStrategyID: "route_1",
	}); err == nil || !strings.Contains(err.Error(), "list management route strategy detail bindings") {
		t.Fatalf("binding error = %v", err)
	}
}

type managementRouteStrategyListRecord struct {
	ID                string
	SystemAccountID   string
	SystemAccountName string
	Name              string
	Description       pgtype.Text
	Mode              string
	Status            string
	IsDefault         bool
	ConfigJSON        pgtype.Text
	CreatedAt         pgtype.Timestamptz
	UpdatedAt         pgtype.Timestamptz
}

type managementRouteStrategyListDetailQueriesStub struct {
	listCalls           []string
	listRecord          managementRouteStrategyListRecord
	lastSystemAccountID string
	lastKeyword         string
	lastKeywordUpper    string
	lastMode            string
	lastStatus          string
	lastOffset          int64
	lastLimit           int64
	enrichmentParams    postgresqueries.ListManagementRouteStrategyListEnrichmentParams
	enrichmentRows      []postgresqueries.ListManagementRouteStrategyListEnrichmentRow
	enrichmentErr       error
	detailParams        []postgresqueries.FindManagementRouteStrategyDetailParams
	detailRow           postgresqueries.FindManagementRouteStrategyDetailRow
	detailErr           error
	detailBindingParams []postgresqueries.ListManagementRouteStrategyDetailBindingsParams
	detailBindings      []postgresqueries.ListManagementRouteStrategyDetailBindingsRow
	detailBindingErr    error
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementRouteStrategies(
	_ context.Context,
	arg postgresqueries.ListManagementRouteStrategiesParams,
) ([]postgresqueries.ListManagementRouteStrategiesRow, error) {
	s.captureListCall("global", "", "", "", arg.Mode, arg.Status, arg.RowOffset, arg.RowLimit)
	row := s.listRecord
	return []postgresqueries.ListManagementRouteStrategiesRow{
		managementRouteStrategyGlobalRow(row),
		managementRouteStrategyGlobalRow(row),
	}, nil
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementOwnedRouteStrategies(
	_ context.Context,
	arg postgresqueries.ListManagementOwnedRouteStrategiesParams,
) ([]postgresqueries.ListManagementOwnedRouteStrategiesRow, error) {
	s.captureListCall("owner", arg.SystemAccountID, "", "", arg.Mode, arg.Status, arg.RowOffset, arg.RowLimit)
	row := s.listRecord
	return []postgresqueries.ListManagementOwnedRouteStrategiesRow{
		managementRouteStrategyOwnerRow(row),
		managementRouteStrategyOwnerRow(row),
	}, nil
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementRouteStrategiesByKeyword(
	_ context.Context,
	arg postgresqueries.ListManagementRouteStrategiesByKeywordParams,
) ([]postgresqueries.ListManagementRouteStrategiesByKeywordRow, error) {
	s.captureListCall("global_keyword", "", arg.Keyword, arg.KeywordUpper, arg.Mode, arg.Status, arg.RowOffset, arg.RowLimit)
	row := s.listRecord
	return []postgresqueries.ListManagementRouteStrategiesByKeywordRow{
		managementRouteStrategyKeywordRow(row),
		managementRouteStrategyKeywordRow(row),
	}, nil
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementOwnedRouteStrategiesByKeyword(
	_ context.Context,
	arg postgresqueries.ListManagementOwnedRouteStrategiesByKeywordParams,
) ([]postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow, error) {
	s.captureListCall("owner_keyword", arg.SystemAccountID, arg.Keyword, arg.KeywordUpper, arg.Mode, arg.Status, arg.RowOffset, arg.RowLimit)
	row := s.listRecord
	return []postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow{
		managementRouteStrategyOwnerKeywordRow(row),
		managementRouteStrategyOwnerKeywordRow(row),
	}, nil
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementRouteStrategyListEnrichment(
	_ context.Context,
	arg postgresqueries.ListManagementRouteStrategyListEnrichmentParams,
) ([]postgresqueries.ListManagementRouteStrategyListEnrichmentRow, error) {
	s.enrichmentParams = arg
	return s.enrichmentRows, s.enrichmentErr
}

func (s *managementRouteStrategyListDetailQueriesStub) FindManagementRouteStrategyDetail(
	_ context.Context,
	arg postgresqueries.FindManagementRouteStrategyDetailParams,
) (postgresqueries.FindManagementRouteStrategyDetailRow, error) {
	s.detailParams = append(s.detailParams, arg)
	return s.detailRow, s.detailErr
}

func (s *managementRouteStrategyListDetailQueriesStub) ListManagementRouteStrategyDetailBindings(
	_ context.Context,
	arg postgresqueries.ListManagementRouteStrategyDetailBindingsParams,
) ([]postgresqueries.ListManagementRouteStrategyDetailBindingsRow, error) {
	s.detailBindingParams = append(s.detailBindingParams, arg)
	return s.detailBindings, s.detailBindingErr
}

func (s *managementRouteStrategyListDetailQueriesStub) captureListCall(
	name string,
	systemAccountID string,
	keyword string,
	keywordUpper string,
	mode string,
	status string,
	offset int64,
	limit int64,
) {
	s.listCalls = append(s.listCalls, name)
	s.lastSystemAccountID = systemAccountID
	s.lastKeyword = keyword
	s.lastKeywordUpper = keywordUpper
	s.lastMode = mode
	s.lastStatus = status
	s.lastOffset = offset
	s.lastLimit = limit
}

func managementRouteStrategyGlobalRow(row managementRouteStrategyListRecord) postgresqueries.ListManagementRouteStrategiesRow {
	return postgresqueries.ListManagementRouteStrategiesRow{
		ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
		Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
		IsDefault: row.IsDefault, ConfigJson: row.ConfigJSON, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func managementRouteStrategyOwnerRow(row managementRouteStrategyListRecord) postgresqueries.ListManagementOwnedRouteStrategiesRow {
	return postgresqueries.ListManagementOwnedRouteStrategiesRow{
		ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
		Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
		IsDefault: row.IsDefault, ConfigJson: row.ConfigJSON, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func managementRouteStrategyKeywordRow(row managementRouteStrategyListRecord) postgresqueries.ListManagementRouteStrategiesByKeywordRow {
	return postgresqueries.ListManagementRouteStrategiesByKeywordRow{
		ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
		Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
		IsDefault: row.IsDefault, ConfigJson: row.ConfigJSON, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func managementRouteStrategyOwnerKeywordRow(row managementRouteStrategyListRecord) postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow {
	return postgresqueries.ListManagementOwnedRouteStrategiesByKeywordRow{
		ID: row.ID, SystemAccountID: row.SystemAccountID, SystemAccountName: row.SystemAccountName,
		Name: row.Name, Description: row.Description, Mode: row.Mode, Status: row.Status,
		IsDefault: row.IsDefault, ConfigJson: row.ConfigJSON, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

var _ managementRouteStrategyListDetailQueries = (*managementRouteStrategyListDetailQueriesStub)(nil)
