package managementroutestrategies

import (
	"context"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceOptionsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &routeStrategyOptionStoreStub{
		options: []port.ManagementRouteStrategyOption{
			{
				ID:                "route_default",
				SystemAccountID:   "sys_admin",
				SystemAccountName: "管理员",
				Name:              "默认路由",
				Mode:              "normal",
				Status:            "active",
				IsDefault:         true,
			},
		},
	}
	service := NewService(store)

	options, err := service.Options(context.Background(), OptionListInput{
		SystemAccountID:            " sys_admin ",
		IncludeSystemAccountFields: true,
		IDs:                        []string{" route_default ", "route_default", "", "route_disabled"},
		Keyword:                    " 默认 ",
		Limit:                      500,
		ActiveOnly:                 false,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}

	if store.input.SystemAccountID != "sys_admin" ||
		store.input.Keyword != "默认" ||
		store.input.Limit != 100 ||
		store.input.ActiveOnly ||
		!store.input.IncludeSystemAccountFields {
		t.Fatalf("store input = %+v", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "route_default" || store.input.IDs[1] != "route_disabled" {
		t.Fatalf("store ids = %#v", store.input.IDs)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want 1", len(options))
	}
	got := options[0]
	if got.SystemAccountID != "sys_admin" || got.SystemAccountName != "管理员" || got.Mode != "normal" || !got.IsDefault {
		t.Fatalf("option = %+v", got)
	}
}

func TestServiceOptionsDefaults(t *testing.T) {
	store := &routeStrategyOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

type routeStrategyOptionStoreStub struct {
	input   port.ManagementRouteStrategyOptionListInput
	options []port.ManagementRouteStrategyOption
	err     error
}

func (s *routeStrategyOptionStoreStub) ListManagementRouteStrategyOptions(_ context.Context, input port.ManagementRouteStrategyOptionListInput) ([]port.ManagementRouteStrategyOption, error) {
	s.input = input
	return s.options, s.err
}
