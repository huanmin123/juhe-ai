package managementproxies

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

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
	input   port.ManagementProxyOptionListInput
	options []port.ManagementProxyOption
	err     error
}

func (s *proxyOptionStoreStub) ListManagementProxyOptions(_ context.Context, input port.ManagementProxyOptionListInput) ([]port.ManagementProxyOption, error) {
	s.input = input
	return s.options, s.err
}
