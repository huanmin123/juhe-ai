// account_health_probe_request_outbox 跨进程 DDL 的防漂移 golden 门禁。
//
// gateway 与 jobs 是两个独立 Go module（backend-go-gateway /
// backend-go-jobs，经 backend-go/go.work 并存），各自维护 CREATE TABLE /
// CREATE INDEX 文本（两侧运行时各自幂等建表），漂移会让一侧建出另一侧读
// 不到的表形状，表现为 SQL 报错。
//
// 本测试用 go/parser 解析两侧源文件的 DDL 常量字符串（规范化行首尾空白后
// 逐行比对）。gateway module 未检出（例如只单独检出 jobs module）时
// t.Skip，monorepo 完整检出的开发与 CI 环境生效。
//
// 用量记录字段契约自 2026-11-09 T188 起由类型别名
// （gatewayusage.UsageRecordInput = usagewriter.UsageRecordInput）在编译期
// 保证，不再需要手工 golden 对比。
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestCrossProcessContractGolden 是 account_health_probe_request_outbox
// DDL 的 golden 对比入口。
func TestCrossProcessContractGolden(t *testing.T) {
	root, ok := crossContractBackendGoRoot(t)
	if !ok {
		return
	}
	gatewayOutbox := filepath.Join(root, "projects", "gateway", "cmd", "juhe-ai-gateway", "chain_request_failure_health.go")
	jobsOutbox := filepath.Join(root, "projects", "jobs", "cmd", "juhe-ai-jobs", "worker_health_probe_outbox.go")

	// gateway 侧属于另一个 module：未检出时跳过（monorepo 完整检出时生效）。
	if !crossContractFileExists(gatewayOutbox) {
		t.Skipf("gateway 契约源未检出（monorepo 完整检出时本测试生效）: %s", gatewayOutbox)
	}
	// jobs 侧属于本 module：缺失即本 module 自身损坏，不是检出范围问题。
	if !crossContractFileExists(jobsOutbox) {
		t.Fatalf("jobs module 自身契约源缺失: %s", jobsOutbox)
	}

	for _, pair := range []struct {
		label        string
		gatewayConst string
		jobsConst    string
	}{
		{"CREATE TABLE account_health_probe_request_outbox", "chainProbeRequestOutboxSchema", "healthProbeOutboxSchema"},
		{"CREATE INDEX idx_account_health_probe_request_outbox_pending", "chainProbeRequestOutboxIndex", "healthProbeOutboxIndex"},
	} {
		gatewayDDL := crossContractNormalizeDDL(crossContractConstString(t, gatewayOutbox, pair.gatewayConst))
		jobsDDL := crossContractNormalizeDDL(crossContractConstString(t, jobsOutbox, pair.jobsConst))
		crossContractAssertSameDDL(t, pair.label, gatewayOutbox, pair.gatewayConst, gatewayDDL, jobsOutbox, pair.jobsConst, jobsDDL)
	}
}

// crossContractBackendGoRoot 用 runtime.Caller(0) 取测试文件自身路径，向上
// 定位 backend-go workspace 根（本测试文件位于 projects/jobs/cmd/
// juhe-ai-jobs/ 下，包目录向上 4 级）。以 go.work + projects/{gateway,jobs}
// 双 module 标志文件探测而不是硬编码层数，容忍目录层级调整；最多向上 8
// 级，仍未命中则 t.Skip（gateway 侧可能未检出）。
func crossContractBackendGoRoot(t *testing.T) (string, bool) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) 无法定位测试文件路径")
	}
	dir := filepath.Dir(thisFile)
	for level := 0; level < 8; level++ {
		if crossContractFileExists(filepath.Join(dir, "go.work")) &&
			crossContractFileExists(filepath.Join(dir, "projects", "gateway", "go.mod")) &&
			crossContractFileExists(filepath.Join(dir, "projects", "jobs", "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skipf("未定位到 backend-go workspace 根（monorepo 完整检出时生效），测试文件: %s", thisFile)
	return "", false
}

func crossContractFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func crossContractParse(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	return file
}

func crossContractConstString(t *testing.T, path, constName string) string {
	t.Helper()
	file := crossContractParse(t, path)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != constName {
					continue
				}
				if len(vs.Values) <= i {
					t.Fatalf("%s: 常量 %s 缺少显式值", path, constName)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: 常量 %s 不是字符串字面量", path, constName)
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: 常量 %s 字符串字面量解码失败: %v", path, constName, err)
				}
				return value
			}
		}
	}
	t.Fatalf("%s: 未找到字符串常量 %s", path, constName)
	return ""
}

// crossContractNormalizeDDL 规范化 DDL 文本用于对比：按行拆分、每行去
// 首尾空白、丢弃空行。行序、大小写、列序与约束文本全部保持原样参与
// 对比（缩进差异不构成漂移）。
func crossContractNormalizeDDL(ddl string) []string {
	lines := strings.Split(ddl, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// crossContractAssertSameDDL 断言两侧 DDL 规范化行序列一致，失败时输出
// 第一个差异行（含行号与两侧该行原文）。
func crossContractAssertSameDDL(t *testing.T, label, gatewayPath, gatewayConst string, gatewayDDL []string, jobsPath, jobsConst string, jobsDDL []string) {
	t.Helper()
	count := len(gatewayDDL)
	if len(jobsDDL) > count {
		count = len(jobsDDL)
	}
	for i := 0; i < count; i++ {
		var haveGateway, haveJobs bool
		var gwLine, jbLine string
		if i < len(gatewayDDL) {
			haveGateway, gwLine = true, gatewayDDL[i]
		}
		if i < len(jobsDDL) {
			haveJobs, jbLine = true, jobsDDL[i]
		}
		if haveGateway && haveJobs && gwLine == jbLine {
			continue
		}
		gwDisplay, jbDisplay := gwLine, jbLine
		if !haveGateway {
			gwDisplay = "<缺失（gateway 侧没有该行）>"
		}
		if !haveJobs {
			jbDisplay = "<缺失（jobs 侧没有该行）>"
		}
		t.Fatalf("契约 2（%s）两侧 DDL 第 %d 行不一致（gateway %s 共 %d 行，jobs %s 共 %d 行）:\n  gateway %s %s = %s\n  jobs    %s %s = %s\n两侧运行时各自幂等建表：DDL 漂移会让一侧建出另一侧读不到的表形状（SQL 报错），必须同一次提交内同步两侧。",
			label, i+1, gatewayConst, len(gatewayDDL), jobsConst, len(jobsDDL),
			gatewayPath, gatewayConst, gwDisplay,
			jobsPath, jobsConst, jbDisplay)
	}
}
