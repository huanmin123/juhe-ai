package app

import (
	"os"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestNewManagementAPIHandlerExternalIntegrationSourceBuiltInResetOptIn(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.ExternalSourceBuiltInResetHandler != nil {
		t.Fatal("built-in reset handler created while management API disabled")
	}
	sessionOnly := newManagementAPIHandlerWithPageData(config.Config{ManagementAuthSessionsEnabled: true}, nil, nil, nil, nil, nil, nil, nil, nil)
	if sessionOnly.ExternalSourceBuiltInResetHandler != nil {
		t.Fatal("built-in reset handler created for session-only mode")
	}
	enabled := newManagementAPIHandlerWithPageData(config.Config{ManagementAPIEnabled: true}, nil, nil, nil, nil, nil, nil, nil, nil)
	if enabled.ExternalSourceBuiltInResetHandler == nil {
		t.Fatal("built-in reset handler missing while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"managementexternalintegrationsources.NewBuiltInResetService(store, cfg.Secret)",
		"ManagementExternalSourceBuiltInResetHandler:",
		"managementHandlers.ExternalSourceBuiltInResetHandler",
		"httpapi.NewManagementExternalIntegrationSourceBuiltInResetHandlerWithOperationLog(externalIntegrationSourceBuiltInResetService, operationLogOptions)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing built-in reset wiring %q", required)
		}
	}
}
