package app

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
)

func TestNewPublicAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handler, logQueue, err := newPublicAPIHandler(config.Config{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("newPublicAPIHandler() error = %v", err)
	}
	if handler != nil || logQueue != nil {
		t.Fatalf("newPublicAPIHandler() = (%v, %v), want nil handler and queue when disabled", handler, logQueue)
	}
}

func TestNewPublicAPIHandlerRejectsInvalidQueueURLWhenEnabled(t *testing.T) {
	_, _, err := newPublicAPIHandler(config.Config{
		PublicAPIEnabled: true,
		RedisQueueURL:    "http://127.0.0.1:6379/2",
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_QUEUE_URL") {
		t.Fatalf("newPublicAPIHandler() error = %v, want redis queue url error", err)
	}
}

func TestNewPublicAPIHandlersCoversCatalog(t *testing.T) {
	handlers, err := newPublicAPIHandlers(nil, "12345678901234567890123456789012")
	if err != nil {
		t.Fatalf("newPublicAPIHandlers() error = %v", err)
	}

	endpoints := publicapicatalog.Endpoints()
	if len(handlers) != len(endpoints) {
		t.Fatalf("handlers = %d, want %d", len(handlers), len(endpoints))
	}
	for _, endpoint := range endpoints {
		if handlers[endpoint.ID] == nil {
			t.Fatalf("handler %q is missing", endpoint.ID)
		}
	}
}

func TestNewManagementAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{}, nil)
	if handlers.AuthMiddleware != nil ||
		handlers.ProxyOptionsHandler != nil ||
		handlers.SystemAccountOptionsHandler != nil ||
		handlers.ProviderOptionsHandler != nil ||
		handlers.ProviderModelOptionsHandler != nil ||
		handlers.ProviderModelsHandler != nil ||
		handlers.RouteStrategyOptionsHandler != nil ||
		handlers.MyRouteStrategyOptionsHandler != nil ||
		handlers.GroupOptionsHandler != nil ||
		handlers.MyGroupOptionsHandler != nil ||
		handlers.GroupAccountOptionsHandler != nil ||
		handlers.MyGroupAccountOptionsHandler != nil ||
		handlers.AccountOptionsHandler != nil ||
		handlers.MyAccountOptionsHandler != nil ||
		handlers.AccountTagsHandler != nil ||
		handlers.MyAccountTagsHandler != nil {
		t.Fatal("newManagementAPIHandler() returned middleware or handler while disabled")
	}
}

func TestNewManagementAPIHandlerEnabledReturnsAuthAndManagementOptionsHandlers(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{ManagementAPIEnabled: true}, nil)
	if handlers.AuthMiddleware == nil ||
		handlers.ProxyOptionsHandler == nil ||
		handlers.SystemAccountOptionsHandler == nil ||
		handlers.ProviderOptionsHandler == nil ||
		handlers.ProviderModelOptionsHandler == nil ||
		handlers.ProviderModelsHandler == nil ||
		handlers.RouteStrategyOptionsHandler == nil ||
		handlers.MyRouteStrategyOptionsHandler == nil ||
		handlers.GroupOptionsHandler == nil ||
		handlers.MyGroupOptionsHandler == nil ||
		handlers.GroupAccountOptionsHandler == nil ||
		handlers.MyGroupAccountOptionsHandler == nil ||
		handlers.AccountOptionsHandler == nil ||
		handlers.MyAccountOptionsHandler == nil ||
		handlers.AccountTagsHandler == nil ||
		handlers.MyAccountTagsHandler == nil {
		t.Fatal("newManagementAPIHandler() returned nil middleware or handler while enabled")
	}
}
