package app

import (
	"os"
	"strings"
	"testing"
)

func TestServerWiresManagementResponseInspectionPoliciesWithSharedStoreAndRuntimeInvalidator(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	required := []string{
		`"juhe-ai/backend-go/internal/modules/managementresponseinspectionpolicies"`,
		"managementresponseinspectionpolicies.NewService",
		"ResponseInspectionPoliciesHandler",
		"httpapi.NewManagementResponseInspectionPoliciesHandlerWithOperationLog",
		"systemAccountInvalidator.(managementresponseinspectionpolicies.RuntimeInvalidator)",
		"ManagementResponseInspectionPoliciesHandler:",
	}
	for _, fragment := range required {
		if !strings.Contains(text, fragment) {
			t.Errorf("server.go missing %q", fragment)
		}
	}
}
