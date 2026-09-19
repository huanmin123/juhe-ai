// API Key 投影（apikeys/store.go normalizedRouteStrategyMode）merge 白名单
// 回归：merge 策略行经 list/detail 的 route_strategies join 投影不得报
// 「路由策略模式无效」（设计 B5 第四处枚举，漏加会让绑定 merge 策略的
// API Key 管理面 500）。
package apikeys

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// TestWMMergeNormalizedRouteStrategyMode：白名单函数的 merge 分支与既有回归。
func TestWMMergeNormalizedRouteStrategyMode(t *testing.T) {
	mode, err := normalizedRouteStrategyMode(sql.NullString{String: "merge", Valid: true})
	if err != nil || mode == nil || *mode != "merge" {
		t.Fatalf("merge: mode=%v err=%v", mode, err)
	}
	// NULL/空保持省略（nil, nil）。
	if mode, err := normalizedRouteStrategyMode(sql.NullString{}); mode != nil || err != nil {
		t.Fatalf("NULL: mode=%v err=%v", mode, err)
	}
	if mode, err := normalizedRouteStrategyMode(sql.NullString{String: "", Valid: true}); mode != nil || err != nil {
		t.Fatalf("空串: mode=%v err=%v", mode, err)
	}
	// 未知值仍报 Node 的 throw 文案。
	if _, err := normalizedRouteStrategyMode(sql.NullString{String: "bogus", Valid: true}); err == nil || err.Error() != "路由策略模式无效" {
		t.Fatalf("bogus: err=%v", err)
	}
}

// TestWMMergeStrategyJoinProjection：merge 策略行 join 投影走 list/detail。
func TestWMMergeStrategyJoinProjection(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")

	// seed 一个 merge 策略（含两个启用绑定，语义与 store 校验一致）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, mode, status, is_default, created_at, updated_at)
		VALUES ('rs-merge', ?, '合并策略', 'merge', 'active', 0, ?, ?)`, ownerID, now, now)
	env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES ('rsg-merge-1', 'rs-merge', ?, 'grp-merge-1', 'active', ?, ?)`, ownerID, now, now)
	env.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at)
		VALUES ('rsg-merge-2', 'rs-merge', ?, 'grp-merge-2', 'active', ?, ?)`, ownerID, now, now)

	// 创建绑定该 merge 策略的 API Key。
	mergeID := "rs-merge"
	created, _, err := env.store.Create(context.Background(), CreateInput{Name: "merge-key", RouteStrategyID: &mergeID}, AccessScope{ViewerID: ownerID})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	// list 投影：RouteStrategyMode 必须是 merge（修复前未知值 → 500）。
	page, err := env.store.ListPage(context.Background(), AccessScope{ViewerID: ownerID}, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range page.Items {
		if item.ID == created.ID {
			found = true
			if item.RouteStrategyID != "rs-merge" ||
				item.RouteStrategyMode == nil || *item.RouteStrategyMode != "merge" {
				t.Fatalf("list 投影: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("merge key 必须在列表中: %+v", page.Items)
	}

	// detail 投影同源。
	detail, err := env.store.FindDetail(context.Background(), created.ID, AccessScope{ViewerID: ownerID})
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail == nil || detail.RouteStrategyMode == nil || *detail.RouteStrategyMode != "merge" {
		t.Fatalf("detail 投影: %+v", detail)
	}
}
