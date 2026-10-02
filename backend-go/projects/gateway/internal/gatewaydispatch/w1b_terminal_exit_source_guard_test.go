package gatewaydispatch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestW1bTerminalExitEmissionSourceGuard 是终态出口决策摘要的源码门禁
// （设计文档《网关全链路轨迹日志设计》能力二"准备期终态出口摘要"，
// 2026-10-02 起）：preparation.go 中每个构造
// PreparationResult{Outcome: PreparationOutcomeFallback|PreparationOutcomeCompleted}
// 的非 error return，其紧邻前一条语句必须是 emitTerminalDispatchDecision 调用。
// 行为级断言只覆盖 3 个代表出口（真实控制流），本门禁以 AST 全量校验堵住
// "未来新增终态出口忘记发射"的漂移——出口数与发射数必须相等且 ≥ 已知基线。
func TestW1bTerminalExitEmissionSourceGuard(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "preparation.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 preparation.go 失败（测试需在包目录内运行）: %v", err)
	}

	isTerminalOutcomeIdent := func(expr ast.Expr) bool {
		id, ok := expr.(*ast.Ident)
		return ok && (id.Name == "PreparationOutcomeFallback" || id.Name == "PreparationOutcomeCompleted")
	}

	returnsTerminalResult := func(stmt ast.Stmt) bool {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return false
		}
		lit, ok := ret.Results[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if ok && key.Name == "Outcome" && isTerminalOutcomeIdent(kv.Value) {
				return true
			}
		}
		return false
	}

	isEmissionCall := func(stmt ast.Stmt) bool {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			return false
		}
		call, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		fn, ok := call.Fun.(*ast.Ident)
		return ok && fn.Name == "emitTerminalDispatchDecision"
	}

	const minKnownExits = 15 // 2026-10-02 基线：fallback 6 + completed 9
	exits, guarded := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			if !returnsTerminalResult(stmt) {
				continue
			}
			exits++
			switch {
			case i == 0:
				t.Errorf("%s: 终态 return（块内首条语句）前无 emitTerminalDispatchDecision 调用", fset.Position(stmt.Pos()))
			case !isEmissionCall(block.List[i-1]):
				t.Errorf("%s: 终态 return 的紧邻前一条语句不是 emitTerminalDispatchDecision 调用", fset.Position(stmt.Pos()))
			default:
				guarded++
			}
		}
		return true
	})

	if exits == 0 {
		t.Fatal("preparation.go 中未发现任何终态出口——门禁自身失配（解析目标或常量名变更？），禁止静默通过")
	}
	if exits < minKnownExits {
		t.Errorf("终态出口数 %d 低于已知基线 %d——出口被误删时本门禁应显式更新基线", exits, minKnownExits)
	}
	if exits != guarded {
		t.Errorf("终态出口 %d 处，仅 %d 处有紧邻发射调用", exits, guarded)
	}
	t.Logf("终态出口 %d 处全部带紧邻 emitTerminalDispatchDecision 发射（基线 %d）", exits, minKnownExits)
}
