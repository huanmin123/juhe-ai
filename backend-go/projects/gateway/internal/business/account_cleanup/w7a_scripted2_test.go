package accountcleanup

import (
	"context"
	"database/sql/driver"
	"testing"
)

func TestW7AScriptedQueryAuthorizationsArms(t *testing.T) {
	ctx := context.Background()
	cols := []string{"id", "resource_id", "grantee_system_account_id"}
	row := []driver.Value{"auth-1", "acct", "grantee"}
	// explicitIDs 分支。
	ok := w7aOpen(t, []w7aStep{{cols: cols, rows: [][]driver.Value{row}}})
	rows, err := ok.queryAuthorizations(ctx, nil, []string{"auth-1"})
	if err != nil || len(rows) != 1 || rows[0].ID != "auth-1" {
		t.Fatalf("explicit rows=%+v err=%v", rows, err)
	}
	// 空入参：两个 chunk 循环全部跳过。
	empty := w7aOpen(t, nil)
	if rows, err := empty.queryAuthorizations(ctx, nil, nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty rows=%+v err=%v", rows, err)
	}
	errHit := w7aOpen(t, []w7aStep{{rowsErr: w7aErrBoom}})
	if _, err := errHit.queryAuthorizations(ctx, nil, []string{"auth-1"}); err == nil {
		t.Fatal("explicit query error must fail")
	}
	// queryGrantIDs 实例分支错误臂。
	grantErr := w7aOpen(t, []w7aStep{{rowsErr: w7aErrBoom}})
	if _, err := grantErr.queryGrantIDs(ctx, nil, []string{"auth-1"}, true); err == nil {
		t.Fatal("instance grant query error must fail")
	}
}

func TestW7AScriptedBuildCandidateArms(t *testing.T) {
	ctx := context.Background()
	rootRow := accountRow{ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("d"), UpdatedAt: nullStringV("u")}
	relatedCols := []string{"id", "system_account_id", "authorization_instance_authorization_id", "deleted_at", "updated_at"}
	authCols := []string{"id", "resource_id", "grantee_system_account_id"}
	teamCols := []string{"authorization_id", "source_team_id"}
	grantCols := []string{"id"}
	happy := []w7aStep{
		{cols: relatedCols, rows: nil},
		{cols: authCols, rows: [][]driver.Value{{"auth-1", "root", "grantee"}}},
		{cols: teamCols, rows: [][]driver.Value{{"auth-1", "t1"}}},
		{cols: grantCols, rows: [][]driver.Value{{"grant-1"}}},
	}
	ok := w7aOpen(t, happy)
	cand, err := ok.buildCandidate(ctx, rootRow)
	if err != nil || len(cand.AuthorizationIDs) != 1 || len(cand.TeamScopeIDs) != 1 || cand.TeamScopeIDs[0] != "root:t1" {
		t.Fatalf("cand=%+v err=%v", cand, err)
	}
	// 各阶段查询错误。
	for _, tc := range []struct {
		name  string
		steps []w7aStep
	}{
		{"related", []w7aStep{{rowsErr: w7aErrBoom}}},
		{"authorizations", []w7aStep{{cols: relatedCols, rows: nil}, {rowsErr: w7aErrBoom}}},
		{"teams", []w7aStep{{cols: relatedCols, rows: nil}, {cols: authCols, rows: nil}, {rowsErr: w7aErrBoom}}},
		{"grants", []w7aStep{{cols: relatedCols, rows: nil}, {cols: authCols, rows: [][]driver.Value{{"auth-1", "root", "grantee"}}}, {cols: teamCols, rows: [][]driver.Value{{"auth-1", "t1"}}}, {rowsErr: w7aErrBoom}}},
	} {
		store := w7aOpen(t, tc.steps)
		if _, err := store.buildCandidate(ctx, rootRow); err == nil {
			t.Fatalf("buildCandidate %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedRevokeAndTombstoneArms(t *testing.T) {
	ctx := context.Background()
	row := accountRow{ID: "orphan", SystemAccountID: "sys", AuthorizationInstanceAuthID: nullStringV("auth-o"), UpdatedAt: nullStringV("u")}
	// revokeInstance：授权行查询错误（非 NoRows）。
	selErr := w7aOpen(t, []w7aStep{{beginErr: nil}, {contains: "SELECT resource_type,resource_id", rowsErr: w7aErrBoom}})
	if _, err := selErr.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("resource select error must fail")
	}
	// 资源为 team：跳过 revokeAccountAuthorizations，直接走来源/授权撤销。
	team := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: []string{"resource_type", "resource_id"}, rows: [][]driver.Value{{"team", "t-1"}}},
		{rowsErr: w7aErrBoom}, // 来源撤销失败
	})
	if _, err := team.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("team revoke failure must fail")
	}
	// 账户型资源：撤销链第二段更新失败。
	acct := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: []string{"resource_type", "resource_id"}, rows: [][]driver.Value{{"account", "t-1"}}},
		{affected: 1},
		{rowsErr: w7aErrBoom},
	})
	if _, err := acct.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("account auths update failure must fail")
	}
	// 重读扫描错误 / 已删除行提交失败 / 更新错误 / RowsAffected 错误 / 提交错误。
	accCols := []string{"authorization_instance_authorization_id", "authorization_instance_source_account_id", "deleted_at", "updated_at"}
	scan := w7aOpen(t, []w7aStep{{beginErr: nil}, {cols: accCols, rows: [][]driver.Value{{nil, nil, nil, nil}}}})
	if _, err := scan.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("re-read scan error must fail")
	}
	deletedCommit := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{{"auth-o", nil, "d", "u"}}},
		{commitErr: w7aErrBoom},
	})
	changed, err := deletedCommit.softDeleteOrphan(ctx, row)
	if changed || err == nil {
		t.Fatalf("already-deleted commit err=%v changed=%v", err, changed)
	}
	updateErr := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{{"auth-o", nil, nil, "u"}}},
		{execErr: w7aErrBoom},
	})
	if _, err := updateErr.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("tombstone update error must fail")
	}
	rowsAffectedErr := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{{"auth-o", nil, nil, "u"}}},
		{affectedErr: w7aErrBoom},
	})
	if _, err := rowsAffectedErr.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("tombstone RowsAffected error must fail")
	}
	commitErr := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{{"auth-o", nil, nil, "u"}}},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{commitErr: w7aErrBoom},
	})
	if _, err := commitErr.softDeleteOrphan(ctx, row); err == nil {
		t.Fatal("commit error must fail")
	}
}

func TestW7AScriptedDeleteBusinessPostgresArm(t *testing.T) {
	ctx := context.Background()
	db := w7aOpenDB(t, []w7aStep{
		{beginErr: nil},
		{cols: []string{"id", "system_account_id", "authorization_instance_authorization_id", "authorization_instance_source_account_id", "deleted_at", "updated_at"}, rows: [][]driver.Value{{"root", "sys", nil, nil, "d", "u"}}},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{commitErr: w7aErrBoom},
	})
	store, err := New(db, Postgres, "juhe_business", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	cand := candidate{row: accountRow{ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("d"), UpdatedAt: nullStringV("u")}, AccountIDs: []string{"root"}}
	if _, err := store.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("pg delete business commit failure must fail")
	}
}
