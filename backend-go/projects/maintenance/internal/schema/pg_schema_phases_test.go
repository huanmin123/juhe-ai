package schema

import (
	"strings"
	"testing"
)

// 相间顺序静态测试（升级兼容与迁移账本设计 §7.3）：分相执行后跨相相对顺序
// 与 Node 单遍序不再一致，合法性钉在「破坏性删除对象不被任何加法语句再次
// 触碰」上——否则加法相先行会重建破坏相将要删除的对象（All 单遍序下二者
// 顺序执行结果不同）。新增破坏性语句或触碰同对象的加法语句都会被本测试拦截。
// 2026-10-30 B09 退役 authorization_*_usage_range_windows 两表后，当前
// 破坏性目标为 2 条 DROP TABLE（juhe_stats），加法清单不得再触碰同表。
func TestPhaseOrderSafety(t *testing.T) {
	for _, target := range DestructiveTargets() {
		if target.Table == "" {
			t.Fatalf("破坏性语句目标解析失败（table 为空）: %+v", target)
		}
		for _, statement := range postgresSchemaStatements {
			if isDestructiveDDL(statement.SQL) {
				continue
			}
			// 加法语句同时点名同表与同列（DROP COLUMN 场景）视为触碰；
			// DROP TABLE 场景只要求加法语句不重建同表。
			if !strings.Contains(statement.SQL, target.Table) {
				continue
			}
			if target.Column == "" || strings.Contains(statement.SQL, target.Column) {
				t.Fatalf("相间顺序冲突：破坏性目标 %s.%s(%s) 被加法语句 %s 触碰——"+
					"分相执行下加法相先行将产生与 EnsureAll 不同的终态",
					target.Schema, target.Table, target.Column, statement.Source)
			}
		}
	}
}

// 分相过滤的纯函数语义：Additive 与 Destructive 不相交且并集为 All。
func TestPhasePartition(t *testing.T) {
	additive, destructive := 0, 0
	for _, statement := range postgresSchemaStatements {
		if isDestructiveDDL(statement.SQL) {
			destructive++
		} else {
			additive++
		}
	}
	// 2026-10-30 B08 撤除 12 条幂等收列语句（业务 9 + codex 3）后破坏相一度
	// 清零；2026-10-30 B09 退役 authorization_*_usage_range_windows 两表，
	// 当前破坏性语句为 2 条 DROP TABLE。
	if destructive != 2 {
		t.Fatalf("破坏性语句数 = %d, want 2", destructive)
	}
	if additive+destructive != len(postgresSchemaStatements) {
		t.Fatalf("分相划分不完整：additive %d + destructive %d != total %d",
			additive, destructive, len(postgresSchemaStatements))
	}
}

// 目标解析抽核：2026-10-30 B09 退役 authorization_*_usage_range_windows 两表
// 后，当前 2 条破坏性目标均为 DROP TABLE（juhe_stats，列名为空属该形态预期，
// 见 DestructiveTarget）；循环体保留，新增破坏性语句时按统一形态解析校验。
func TestDestructiveTargetsParse(t *testing.T) {
	targets := DestructiveTargets()
	if len(targets) != 2 {
		t.Fatalf("解析目标数 = %d, want 2", len(targets))
	}
	for _, target := range targets {
		if target.Table == "" {
			t.Fatalf("目标缺表名: %+v", target)
		}
		// 当前目标全部为 DROP TABLE 形态：列名为空属预期；引入 DROP COLUMN
		// 形态时改为要求列名非空。
		if target.Column != "" {
			t.Fatalf("DROP TABLE 目标不应带列名: %+v", target)
		}
		if target.Schema != "juhe_business" && target.Schema != "juhe_codex_context" && target.Schema != "juhe_stats" {
			t.Fatalf("目标 schema 越界: %+v", target)
		}
	}
}
