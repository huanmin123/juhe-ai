package groupdirtycursor

import (
	"strings"
	"testing"
)

func TestPostgresQualificationAndPlaceholderContract(t *testing.T) {
	s := &Store{mode: Postgres, schema: "juhe_business"}
	if got := s.table("group_account_stats_dirty"); got != "juhe_business.group_account_stats_dirty" {
		t.Fatalf("table=%s", got)
	}
	query := s.bind("DELETE FROM " + s.dirtyTable() + " WHERE group_id=? AND updated_at=?")
	if !strings.Contains(query, "juhe_business.group_account_stats_dirty") || !strings.Contains(query, "$1") || !strings.Contains(query, "$2") || strings.Contains(query, "?") {
		t.Fatalf("query=%s", query)
	}
	selectQuery := s.bind("SELECT reason FROM " + s.dirtyTable() + " WHERE group_id=? FOR UPDATE")
	if selectQuery != "SELECT reason FROM juhe_business.group_account_stats_dirty WHERE group_id=$1 FOR UPDATE" {
		t.Fatalf("select=%s", selectQuery)
	}
}

func TestSQLiteKeepsQuestionPlaceholders(t *testing.T) {
	s := &Store{mode: SQLite}
	query := s.bind("UPDATE " + s.dirtyTable() + " SET reason=?,updated_at=? WHERE group_id=?")
	if query != "UPDATE group_account_stats_dirty SET reason=?,updated_at=? WHERE group_id=?" {
		t.Fatalf("query=%s", query)
	}
}
