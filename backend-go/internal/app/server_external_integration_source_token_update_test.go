package app

import (
	"os"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestNewManagementAPIHandlerExternalIntegrationSourceTokenUpdateOptIn(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.ExternalSourceTokenUpdateHandler != nil {
		t.Fatal("token update handler created while management API disabled")
	}
	sessionOnly := newManagementAPIHandlerWithPageData(config.Config{ManagementAuthSessionsEnabled: true}, nil, nil, nil, nil, nil, nil, nil, nil)
	if sessionOnly.ExternalSourceTokenUpdateHandler != nil {
		t.Fatal("token update handler created for session-only mode")
	}
	enabled := newManagementAPIHandlerWithPageData(config.Config{ManagementAPIEnabled: true}, nil, nil, nil, nil, nil, nil, nil, nil)
	if enabled.ExternalSourceTokenUpdateHandler == nil {
		t.Fatal("token update handler missing while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"managementexternalintegrationsources.NewTokenUpdateService(store)",
		"ManagementExternalSourceTokenUpdateHandler:",
		"managementHandlers.ExternalSourceTokenUpdateHandler",
		"httpapi.NewManagementExternalIntegrationSourceTokenUpdateHandlerWithOperationLog(externalIntegrationSourceTokenUpdateService, operationLogOptions)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing token update wiring %q", required)
		}
	}
}
