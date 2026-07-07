package managementsystemaccounts

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestOptionsNormalizesInputAndMapsOptions(t *testing.T) {
	store := &systemAccountOptionStoreStub{
		options: []port.ManagementSystemAccountOption{{
			ID:          "sys_user",
			Username:    "user",
			DisplayName: "用户",
			Status:      "active",
		}},
	}
	service := NewService(store)

	got, err := service.Options(context.Background(), OptionListInput{
		IDs:     []string{" sys_user ", "sys_user", "", "sys_disabled"},
		Keyword: "  用户  ",
		Limit:   500,
	})
	if err != nil {
		t.Fatalf("Options() error = %v", err)
	}
	if store.input.Keyword != "用户" || store.input.Limit != 50 {
		t.Fatalf("store input = %+v, want trimmed keyword and limit 50", store.input)
	}
	if len(store.input.IDs) != 2 || store.input.IDs[0] != "sys_user" || store.input.IDs[1] != "sys_disabled" {
		t.Fatalf("ids = %#v", store.input.IDs)
	}
	if len(got) != 1 || got[0].ID != "sys_user" || got[0].Username != "user" || got[0].DisplayName != "用户" || got[0].Status != "active" {
		t.Fatalf("Options() = %+v", got)
	}
}

func TestOptionsDefaultsLimit(t *testing.T) {
	store := &systemAccountOptionStoreStub{}
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
	service := NewService(&systemAccountOptionStoreStub{err: want})

	_, err := service.Options(context.Background(), OptionListInput{})

	if !errors.Is(err, want) {
		t.Fatalf("Options() error = %v, want %v", err, want)
	}
}

type systemAccountOptionStoreStub struct {
	input   port.ManagementSystemAccountOptionListInput
	options []port.ManagementSystemAccountOption
	err     error
}

func (s *systemAccountOptionStoreStub) ListManagementSystemAccountOptions(_ context.Context, input port.ManagementSystemAccountOptionListInput) ([]port.ManagementSystemAccountOption, error) {
	s.input = input
	return s.options, s.err
}
