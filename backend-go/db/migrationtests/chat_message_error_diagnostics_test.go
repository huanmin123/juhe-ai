package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestChatMessageErrorDiagnosticsMigrationPreservesSchemaOwnershipBoundary(t *testing.T) {
	const migrationName = "000077_w2_chat_message_error_diagnostics.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	if strings.Count(sql, "to_regclass('juhe_chat.chat_messages') IS NOT NULL") != 2 {
		t.Fatalf("%s must guard both Up and Down when the Node-owned Chat table is absent", migrationName)
	}
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS error_message text",
		"DROP COLUMN IF EXISTS error_message",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
}
