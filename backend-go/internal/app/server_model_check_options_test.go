package app

import (
	"os"
	"strings"
	"testing"
)

func TestServerWiresManagementModelCheckOptionsReaders(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"managementmodelcheckoptions.NewService()",
		"ManagementModelCheckOptionsHandler:",
		"ManagementMyModelCheckOptionsHandler:",
		"ModelCheckOptionsHandler:",
		"MyModelCheckOptionsHandler:",
		"httpapi.NewManagementModelCheckOptionsHandler(modelCheckOptionsService)",
		"httpapi.NewManagementMyModelCheckOptionsHandler(modelCheckOptionsService)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing model-check options wiring %q", required)
		}
	}
}
