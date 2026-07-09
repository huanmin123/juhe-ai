package managementproxies

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListNormalizesPaginationAndMapsRows(t *testing.T) {
	lastTestedAt := time.Date(2026, 7, 10, 8, 30, 0, 0, time.UTC)
	description := "说明"
	username := "proxy-user"
	latencyMs := 123
	outboundIP := "203.0.113.8"
	outboundRegion := "US"
	lastTestMessage := "ok"
	store := &proxyOptionStoreStub{
		listResult: port.ManagementProxyListResult{
			Items: []port.ManagementProxySummary{
				{
					ID:              "proxy_a",
					Name:            "代理 A",
					Description:     &description,
					Type:            "socks5h",
					Host:            "proxy.example.com",
					Port:            1080,
					Username:        &username,
					Enabled:         true,
					TestStatus:      "passed",
					LatencyMs:       &latencyMs,
					OutboundIP:      &outboundIP,
					OutboundRegion:  &outboundRegion,
					LastTestMessage: &lastTestMessage,
					LastTestedAt:    &lastTestedAt,
				},
			},
			HasMore: true,
		},
	}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Keyword: "  代理  ", Page: 2, PageSize: 500})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Keyword != "代理" || store.listInput.Limit != maxListPageSize+1 || store.listInput.Offset != maxListPageSize {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if got.Page != 2 || got.PageSize != maxListPageSize || got.Total != 202 || !got.HasMore {
		t.Fatalf("pagination = %+v", got)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	item := got.Items[0]
	if item.ID != "proxy_a" ||
		item.Description == nil ||
		*item.Description != "说明" ||
		item.Username == nil ||
		*item.Username != "proxy-user" ||
		item.LatencyMs == nil ||
		*item.LatencyMs != 123 ||
		item.LastTestedAt == nil ||
		!item.LastTestedAt.Equal(lastTestedAt) {
		t.Fatalf("item = %+v", item)
	}
}

func TestListDefaultsPaginationAndDetectsExtraRow(t *testing.T) {
	store := &proxyOptionStoreStub{
		listResult: port.ManagementProxyListResult{
			Items: []port.ManagementProxySummary{
				{ID: "proxy_1", Name: "代理 1"},
				{ID: "proxy_2", Name: "代理 2"},
			},
		},
	}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Page: -10, PageSize: 1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 2 || store.listInput.Offset != 0 {
		t.Fatalf("store list input = %+v", store.listInput)
	}
	if got.Page != 1 || got.PageSize != 1 || got.Total != 2 || !got.HasMore || len(got.Items) != 1 {
		t.Fatalf("result = %+v", got)
	}
}

func TestListClampsDeepPageToWindow(t *testing.T) {
	store := &proxyOptionStoreStub{}
	service := NewService(store)

	got, err := service.List(context.Background(), ListInput{Page: 999, PageSize: 20})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.Limit != 21 || store.listInput.Offset != 980 {
		t.Fatalf("store list input = %+v, want page clamped to 1001 row window", store.listInput)
	}
	if got.Page != 50 || got.PageSize != 20 || got.Total != 980 || got.HasMore {
		t.Fatalf("result = %+v", got)
	}
}

func TestListReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&proxyOptionStoreStub{listErr: want})

	_, err := service.List(context.Background(), ListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("List() error = %v, want %v", err, want)
	}
}

func TestOptionsTrimsKeywordAndClampsLimit(t *testing.T) {
	store := &proxyOptionStoreStub{
		options: []port.ManagementProxyOption{{
			ID:      "proxy_a",
			Name:    "代理 A",
			Type:    "http",
			Enabled: true,
		}},
	}
	service := NewService(store)

	got, err := service.Options(context.Background(), OptionListInput{Keyword: "  代理  ", Limit: 500})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Keyword != "代理" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if len(got) != 1 || got[0].ID != "proxy_a" || got[0].Name != "代理 A" || got[0].Type != "http" || !got[0].Enabled {
		t.Fatalf("Options() = %+v", got)
	}
}

func TestOptionsDefaultsLimit(t *testing.T) {
	store := &proxyOptionStoreStub{}
	service := NewService(store)

	if _, err := service.Options(context.Background(), OptionListInput{Limit: -10}); err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Limit != 50 {
		t.Fatalf("limit = %d, want 50", store.input.Limit)
	}
}

func TestOptionsReturnsStoreError(t *testing.T) {
	want := errors.New("postgres down")
	service := NewService(&proxyOptionStoreStub{err: want})

	_, err := service.Options(context.Background(), OptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("Options() error = %v, want %v", err, want)
	}
}

type proxyOptionStoreStub struct {
	listInput  port.ManagementProxyListInput
	input      port.ManagementProxyOptionListInput
	listResult port.ManagementProxyListResult
	options    []port.ManagementProxyOption
	listErr    error
	err        error
}

func (s *proxyOptionStoreStub) ListManagementProxies(_ context.Context, input port.ManagementProxyListInput) (port.ManagementProxyListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *proxyOptionStoreStub) ListManagementProxyOptions(_ context.Context, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	s.input = input
	return s.options, s.err
}
