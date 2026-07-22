package app

import (
	"os"
	"strings"
	"testing"
)

func TestServerWiresManagementModelCheckReadHandlersOnly(t *testing.T) {
	sourceBytes, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	for _, fragment := range []string{
		`"juhe-ai/backend-go/internal/modules/managementmodelchecks"`,
		`modelCheckService := managementmodelchecks.NewService(store)`,
		`ModelCheckOptionsHandler`,
		`MyModelCheckOptionsHandler`,
		`ModelCheckActiveHandler`,
		`MyModelCheckActiveHandler`,
		`ModelCheckListHandler`,
		`MyModelCheckListHandler`,
		`ModelCheckDetailHandler`,
		`MyModelCheckDetailHandler`,
		`httpapi.NewManagementModelCheckOptionsHandler(modelCheckService)`,
		`httpapi.NewManagementMyModelCheckDetailHandler(modelCheckService)`,
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("server.go missing model-check read wiring %q", fragment)
		}
	}
	for _, forbidden := range []string{
		`NewManagementModelCheckRunHandler`,
		`NewManagementModelCheckStopHandler`,
		`NewManagementModelCheckStreamHandler`,
		`modelCheckWorker`,
		`modelCheckExecutor`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("server.go unexpectedly wires model-check writer/runtime %q", forbidden)
		}
	}
}
