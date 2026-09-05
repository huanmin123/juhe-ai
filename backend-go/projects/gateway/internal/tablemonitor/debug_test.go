package tablemonitor

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDebugMissingSchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/empty.sqlite3?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), `SELECT database_role FROM database_storage_snapshots`)
	fmt.Printf("err=%v\n", err)
	fmt.Printf("typed=%v\n", isSchemaMissing(err))
	_ = rows
}
