package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestManagementStatsOverviewSQLIsBoundedParameterizedAndPreaggregated(t *testing.T) {
	queries := []struct {
		name string
		sql  string
		want []string
	}{
		{name: "summary", sql: managementStatsOverviewSummarySQL, want: []string{"juhe_stats.usage_overview_summary_windows", "system_account_id = $1", "window_key = $2", "start_date = $3", "end_date = $4", "LIMIT 1"}},
		{name: "trend", sql: managementStatsOverviewTrendSQL, want: []string{"juhe_stats.usage_overview_trend_windows", "ORDER BY bucket_key ASC", "LIMIT 744"}},
		{name: "models", sql: managementStatsOverviewModelsSQL, want: []string{"juhe_stats.usage_model_rank_windows", "ORDER BY rank ASC, provider_code ASC, model ASC", "LIMIT 10"}},
		{name: "errors", sql: managementStatsOverviewErrorsSQL, want: []string{"juhe_stats.usage_error_rank_windows", "ORDER BY rank ASC, provider_code ASC, error_code ASC, status_code ASC", "LIMIT 10"}},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			for _, want := range query.want {
				if !strings.Contains(query.sql, want) {
					t.Fatalf("SQL missing %q:\n%s", want, query.sql)
				}
			}
			lower := strings.ToLower(query.sql)
			for _, forbidden := range []string{"usage_records", "usage_stats_daily", "usage_stats_hourly", " group by ", " sum("} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("SQL must not contain %q:\n%s", forbidden, query.sql)
				}
			}
		})
	}
}

func TestReadManagementStatsOverviewUsesOneStableWindowForAllTables(t *testing.T) {
	queries := &managementStatsOverviewQueriesStub{
		summary: port.ManagementStatsOverviewSummaryRow{RequestCount: 2},
		found:   true,
		trend:   []port.ManagementStatsOverviewTrendRow{{StatHour: "2026-07-22T08"}},
		models:  []port.ManagementStatsOverviewModelRow{{ProviderCode: "openai", Model: "gpt-5"}},
		errors:  []port.ManagementStatsOverviewErrorRow{{ProviderCode: "openai", ErrorCode: "upstream_error"}},
	}
	input := port.ManagementStatsOverviewReadInput{SystemAccountID: "sys_1", WindowKey: "2026-07-01:2026-07-22", StartDate: "2026-07-01", EndDate: "2026-07-22"}

	got, err := readManagementStatsOverview(context.Background(), queries, input)

	if err != nil {
		t.Fatalf("readManagementStatsOverview() error = %v", err)
	}
	if got.Summary == nil || got.Summary.RequestCount != 2 || len(got.HourlyTrend) != 1 || len(got.ModelDistribution) != 1 || len(got.Errors) != 1 {
		t.Fatalf("window = %+v", got)
	}
	if queries.summaryInput != input || queries.trendInput != input || queries.modelsInput != input || queries.errorsInput != input {
		t.Fatalf("inputs = %+v / %+v / %+v / %+v", queries.summaryInput, queries.trendInput, queries.modelsInput, queries.errorsInput)
	}
}

func TestReadManagementStatsOverviewReturnsEmptySummaryAndWrapsDependencyErrors(t *testing.T) {
	t.Run("missing summary", func(t *testing.T) {
		got, err := readManagementStatsOverview(context.Background(), &managementStatsOverviewQueriesStub{}, port.ManagementStatsOverviewReadInput{})
		if err != nil || got.Summary != nil || got.HourlyTrend == nil || got.ModelDistribution == nil || got.Errors == nil {
			t.Fatalf("window = %+v, err = %v", got, err)
		}
	})

	readErr := errors.New("read failed")
	for _, stage := range []string{"summary", "trend", "models", "errors"} {
		t.Run(stage, func(t *testing.T) {
			queries := &managementStatsOverviewQueriesStub{found: true, failStage: stage, err: readErr}
			_, err := readManagementStatsOverview(context.Background(), queries, port.ManagementStatsOverviewReadInput{})
			if !errors.Is(err, readErr) || !strings.Contains(err.Error(), stage) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type managementStatsOverviewQueriesStub struct {
	summaryInput port.ManagementStatsOverviewReadInput
	trendInput   port.ManagementStatsOverviewReadInput
	modelsInput  port.ManagementStatsOverviewReadInput
	errorsInput  port.ManagementStatsOverviewReadInput
	summary      port.ManagementStatsOverviewSummaryRow
	found        bool
	trend        []port.ManagementStatsOverviewTrendRow
	models       []port.ManagementStatsOverviewModelRow
	errors       []port.ManagementStatsOverviewErrorRow
	failStage    string
	err          error
}

func (s *managementStatsOverviewQueriesStub) summaryRow(_ context.Context, input port.ManagementStatsOverviewReadInput) (port.ManagementStatsOverviewSummaryRow, bool, error) {
	s.summaryInput = input
	if s.failStage == "summary" {
		return port.ManagementStatsOverviewSummaryRow{}, false, s.err
	}
	return s.summary, s.found, nil
}

func (s *managementStatsOverviewQueriesStub) trendRows(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewTrendRow, error) {
	s.trendInput = input
	if s.failStage == "trend" {
		return nil, s.err
	}
	return s.trend, nil
}

func (s *managementStatsOverviewQueriesStub) modelRows(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewModelRow, error) {
	s.modelsInput = input
	if s.failStage == "models" {
		return nil, s.err
	}
	return s.models, nil
}

func (s *managementStatsOverviewQueriesStub) errorRows(_ context.Context, input port.ManagementStatsOverviewReadInput) ([]port.ManagementStatsOverviewErrorRow, error) {
	s.errorsInput = input
	if s.failStage == "errors" {
		return nil, s.err
	}
	return s.errors, nil
}

var _ managementStatsOverviewQueries = (*managementStatsOverviewQueriesStub)(nil)
