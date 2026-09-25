package chat

import (
	"errors"
	"strings"
	"testing"
)

// Storage-layer faults must not ride the public diagnostic detail; ordinary
// upstream copy keeps the sanitized "；详情：" suffix.
func TestGenerationErrorStorageFaultsDropDetail(t *testing.T) {
	fallback := "生成任务异常结束，请重新发送"
	for _, raw := range []string{
		`pq: duplicate key value violates unique constraint "idx_groups_owner_provider_name_unique"`,
		"SQL logic error: no such table: announcements (1)",
		"constraint failed: UNIQUE constraint failed: system_teams.name (2067)",
		"sql: no rows in result set",
		"ERROR: relation \"juhe_business.groups\" does not exist (SQLSTATE 42P01)",
		"database is locked (5)",
		"INSERT violated CHECK constraint on accounts",
	} {
		got := ClassifyUnknownChatGenerationError(errors.New(raw))
		if got.Code != GenErrInternal || got.Message != fallback {
			t.Fatalf("storage fault %q = %+v", raw, got)
		}
		if strings.Contains(got.Message, "详情") {
			t.Fatalf("storage fault %q leaked detail: %q", raw, got.Message)
		}
	}

	kept := ClassifyUnknownChatGenerationError(errors.New("upstream 响应超时且连接被重置"))
	if kept.Code != GenErrInternal || !strings.Contains(kept.Message, "详情：upstream 响应超时且连接被重置") {
		t.Fatalf("ordinary upstream detail must stay: %+v", kept)
	}
	// Existing contract: non-storage CJK copy keeps its detail.
	generic := ClassifyUnknownChatGenerationError(errors.New("数据库繁忙"))
	if generic.Message != fallback+"；详情：数据库繁忙" {
		t.Fatalf("generic classification changed: %+v", generic)
	}
	// Network codes still classify as stream failures before the storage arm.
	if network := ClassifyUnknownChatGenerationError(errors.New("ECONNRESET")); network.Code != GenErrUpstreamStream {
		t.Fatalf("network classification changed: %+v", network)
	}
}
