package authz

// revision 31 直发+领取（send-and-claim）新流程钉值测试（外部 PG 臂，pgtestkit
// 门控）：钉 CreateOffer 落库形态与 ExpireDueOffers 的 ended_reason 契约。
// 直发个人 pending / 团队 fanout / 终态重发全新 grant / 分组领取 settings 行 /
// targetGroupId 一步绑定与默认组兜底分别由 my_authorizations_write_test.go、
// w13g2_routes_gaps_test.go、instance_provision_test.go、
// authorized_reads_test.go 覆盖；本文件只补其余钉值缺口。
import (
	"context"
	"testing"
	"time"
)

// TestPinTeamOfferFansOutOnePendingSourcePerMember 钉团队直发 fanout：一次
// CreateOffer 对 team 的每个 active 成员各展开一条 pending team source（挂到
// 该成员的 runtime 行上），grant 只有一条 pending。
func TestPinTeamOfferFansOutOnePendingSourcePerMember(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member1", "active")
	f.seedAccount(t, "member2", "active")
	f.seedGroup(t, "grp_team_pin", "owner")
	f.seedTeamWithMember(t, "team_pin", "member1")
	f.seedTeamWithMember(t, "team_pin", "member2")

	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_team_pin",
		GranteeType: "team", TeamID: "team_pin",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusPending || !created.Created {
		t.Fatalf("team offer result = %+v, want pending/created", created)
	}
	if created.SourceID != "" {
		t.Fatalf("team 创建不落单一 SourceID（fanout 到成员）: %q", created.SourceID)
	}
	// 团队 grant 单条 pending。
	var grantCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_grants WHERE id = ? AND status = 'pending'`),
		created.GrantID).Scan(&grantCount); err != nil || grantCount != 1 {
		t.Fatalf("team grant rows = %d, %v", grantCount, err)
	}
	// 每个成员一条 pending team source，挂到该成员自己的 runtime 行。
	var sourceCount int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_sources
		WHERE grant_id = ? AND status = 'pending' AND source_type = 'team' AND source_team_id = 'team_pin'`),
		created.GrantID).Scan(&sourceCount); err != nil || sourceCount != 2 {
		t.Fatalf("pending team sources = %d, want 2（每成员一条）, %v", sourceCount, err)
	}
	var mismatched int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_sources s
		JOIN resource_authorizations r ON r.id = s.authorization_id
		WHERE s.grant_id = ? AND (r.grantee_system_account_id NOT IN ('member1','member2')
			OR r.grantee_system_account_id IS NULL OR r.status <> 'pending')`),
		created.GrantID).Scan(&mismatched); err != nil || mismatched != 0 {
		t.Fatalf("source/runtime 成员配对错位 %d 行, %v", mismatched, err)
	}
	// 成员移除级联（ended_reason=member_removed）后重发：pending grant 仍开放
	// 会被 findOpenOffer 复用（Created=false），且 fanout 只补活跃成员——
	// removed 成员不再新增 source，原 revoked 行保留归档。
	if err := f.store.RevokeTeamSourcesForMember(context.Background(), "team_pin", "member2", "owner"); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE system_team_members SET status = 'removed', removed_at = '2026-01-02T00:00:00Z'
		WHERE system_account_id = 'member2' AND team_id = 'team_pin'`)
	created2, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_team_pin",
		GranteeType: "team", TeamID: "team_pin",
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if created2.Created || created2.GrantID != created.GrantID {
		t.Fatalf("开放 pending grant 必须复用: %+v vs %s", created2, created.GrantID)
	}
	var sourceCount2, revokedMember2 int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_sources
		WHERE grant_id = ? AND status = 'pending' AND source_type = 'team'`), created2.GrantID).
		Scan(&sourceCount2); err != nil || sourceCount2 != 1 {
		t.Fatalf("重发只保留活跃成员 pending source: %d, want 1, %v", sourceCount2, err)
	}
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*) FROM resource_authorization_sources
		WHERE grant_id = ? AND status = 'revoked' AND ended_reason = 'member_removed'`), created2.GrantID).
		Scan(&revokedMember2); err != nil || revokedMember2 != 1 {
		t.Fatalf("removed 成员 source 应为 member_removed 归档: %d, %v", revokedMember2, err)
	}
}

// TestPinExpirySweepMarksSourcesAuthorizationExpired 钉 ExpireDueOffers 的
// ended_reason 契约：grant 翻 expired（ended_reason=authorization_expired）
// 时，同事务把全部非终态 source 也翻 expired 且 ended_reason 一致。
func TestPinExpirySweepMarksSourcesAuthorizationExpired(t *testing.T) {
	f := newFixture(t)
	f.seedAccount(t, "owner", "active")
	f.seedAccount(t, "member", "active")
	f.seedGroup(t, "grp_expiry_pin", "owner")
	f.seedTeamWithMember(t, "team_exp", "member")

	future := f.now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	created, err := f.store.CreateOffer(context.Background(), OfferInput{
		ResourceType: "group", ResourceID: "grp_expiry_pin",
		GranteeType: "team", TeamID: "team_exp",
		ExpiresAt: &future,
	}, "owner", false, "owner")
	if err != nil {
		t.Fatal(err)
	}
	past := f.now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := f.db.Exec(fixtureBind(`UPDATE resource_authorization_grants SET expires_at = ? WHERE id = ?`),
		past, created.GrantID); err != nil {
		t.Fatal(err)
	}

	swept, err := f.store.ExpireDueOffers(context.Background(), f.now.UTC().Format(time.RFC3339Nano), 10)
	if err != nil || swept.GrantsExpired != 1 {
		t.Fatalf("ExpireDueOffers = %+v, %v", swept, err)
	}
	var grantStatus, grantReason string
	if err := f.db.QueryRow(fixtureBind(`SELECT status, COALESCE(ended_reason,'') FROM resource_authorization_grants WHERE id = ?`),
		created.GrantID).Scan(&grantStatus, &grantReason); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusExpired || grantReason != "authorization_expired" {
		t.Fatalf("grant = %s/%s, want expired/authorization_expired", grantStatus, grantReason)
	}
	var sourcesTotal, expiredWithReason int
	if err := f.db.QueryRow(fixtureBind(`SELECT COUNT(*),
			SUM(CASE WHEN status = 'expired' AND ended_reason = 'authorization_expired'
				AND ended_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM resource_authorization_sources WHERE grant_id = ? AND status <> 'revoked'`),
		created.GrantID).Scan(&sourcesTotal, &expiredWithReason); err != nil {
		t.Fatal(err)
	}
	if sourcesTotal == 0 || expiredWithReason != sourcesTotal {
		t.Fatalf("sources = %d/%d 带 expired+authorization_expired", expiredWithReason, sourcesTotal)
	}
}
