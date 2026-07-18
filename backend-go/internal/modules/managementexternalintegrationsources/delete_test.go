package managementexternalintegrationsources

import (
	"context"
	"errors"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestDeleteServiceNormalizesIDAndReturnsDeletedSnapshot(t *testing.T) {
	store := &externalIntegrationSourceDeleteStoreStub{
		result: port.ManagementExternalIntegrationSourceDeleteResult{
			SourceID:   "source_1",
			SourceName: "测试来源",
			TokenCount: 3,
		},
	}
	result, err := NewDeleteService(store).Delete(context.Background(), DeleteInput{
		SourceID: "\uFEFF source_1 \u3000",
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.calls != 1 || store.sourceID != "source_1" {
		t.Fatalf("store calls=%d sourceID=%q", store.calls, store.sourceID)
	}
	if result.SourceID != "source_1" || result.SourceName != "测试来源" || result.TokenCount != 3 {
		t.Fatalf("Delete() result = %#v", result)
	}
}

func TestDeleteServiceRejectsEmptyIDBeforeStore(t *testing.T) {
	store := &externalIntegrationSourceDeleteStoreStub{}
	_, err := NewDeleteService(store).Delete(context.Background(), DeleteInput{SourceID: "\uFEFF\u3000"})
	if !errors.Is(err, ErrDeleteInvalid) {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d", store.calls)
	}
}

func TestDeleteServiceRejectsBuiltInSourceBeforeStore(t *testing.T) {
	store := &externalIntegrationSourceDeleteStoreStub{}
	_, err := NewDeleteService(store).Delete(context.Background(), DeleteInput{SourceID: "extsrc_builtin_test"})
	if !errors.Is(err, ErrBuiltInDeleteRestricted) {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d", store.calls)
	}
}

func TestDeleteServiceMapsStoreErrors(t *testing.T) {
	internalError := errors.New("database unavailable")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: port.ErrManagementExternalIntegrationSourceNotFound, want: ErrNotFound},
		{name: "built in", err: port.ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted, want: ErrBuiltInDeleteRestricted},
		{name: "internal", err: internalError, want: internalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &externalIntegrationSourceDeleteStoreStub{err: test.err}
			_, err := NewDeleteService(store).Delete(context.Background(), DeleteInput{SourceID: "source_1"})
			if !errors.Is(err, test.want) {
				t.Fatalf("Delete() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDeleteServiceRequiresStore(t *testing.T) {
	_, err := NewDeleteService(nil).Delete(context.Background(), DeleteInput{SourceID: "source_1"})
	if err == nil {
		t.Fatal("Delete() error = nil")
	}
}

type externalIntegrationSourceDeleteStoreStub struct {
	sourceID string
	result   port.ManagementExternalIntegrationSourceDeleteResult
	err      error
	calls    int
}

func (s *externalIntegrationSourceDeleteStoreStub) DeleteManagementExternalIntegrationSource(
	_ context.Context,
	sourceID string,
) (port.ManagementExternalIntegrationSourceDeleteResult, error) {
	s.calls++
	s.sourceID = sourceID
	return s.result, s.err
}
