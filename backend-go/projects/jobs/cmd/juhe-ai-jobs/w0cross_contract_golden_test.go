// 跨进程手工契约拷贝的防漂移 golden 门禁。
//
// 背景：gateway 与 jobs 是两个独立 Go module（backend-go-gateway /
// backend-go-jobs，经 backend-go/go.work 并存），彼此不能 import，因此两份
// 跨进程契约只能各自维护手工同步的拷贝，且没有任何编译期联动——漂移不会
// 在构建期暴露，只会在运行时才表现出来：
//
//   - 契约 1（usage-record-spool JSON 记录形状）：gateway 写侧
//     projects/gateway/internal/gatewayusage/records.go 的 UsageRecordInput
//     （json tag 即 usage-record-spool 落盘格式）与 jobs 读侧
//     projects/jobs/internal/usagewriter/records.go 的同名字段结构。漂移
//     先例风险：jobs 读侧缺字段时该字段被静默丢弃（字段静默丢失），极端
//     形状下整行进 .corrupt 隔离，用量与账务出现不可逆缺口；
//   - 契约 2（account_health_probe_request_outbox 交接表 DDL）：gateway
//     写侧 projects/gateway/cmd/juhe-ai-gateway/chain_request_failure_health.go
//     与 jobs 消费侧本目录 worker_health_probe_outbox.go 各自维护 CREATE
//     TABLE / CREATE INDEX 文本（两侧运行时各自幂等建表）。漂移会让一侧
//     建出另一侧读不到的表形状，表现为 SQL 报错。
//
// 本测试用 go/parser 解析两侧源文件做 golden 对比：契约 1 对比
// UsageRecordInput 的字段序列（字段名 + json tag 原文，含顺序与数量），
// 契约 2 对比 DDL 常量字符串（规范化行首尾空白后逐行比对）。gateway
// module 未检出（例如只单独检出 jobs module）时 t.Skip，monorepo 完整
// 检出的开发与 CI 环境生效。
//
// 负向自证（未实际破坏仓库文件；除推演外，已用独立临时目录脚本对篡改
// 副本实测，脚本不进仓库）：任一侧漂移即失败——
//   - 把 gateway 侧 UsageRecordInput 的 TraceID 改名 TraceIDX，或把 json
//     tag `json:"traceId"` 改成 `json:"traceID"`：两侧第 3 个字段的形状
//     不等，测试报「第 3 个字段不一致」并输出两侧值；
//   - 删除一侧任一字段：两侧字段数不等，测试在短侧多出的第一个位置报
//     「<缺失>」；
//   - 把一侧 DDL 的 "reason TEXT NOT NULL" 改成 "reason TEXT"：规范化行
//     diff 在该行报两侧差异行；增删行则在行数差处报「<缺失>」。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestCrossProcessContractGolden 是两份跨进程手工契约拷贝的 golden 对比
// 入口：子测试分别覆盖 usage-record-spool JSON 记录形状与
// account_health_probe_request_outbox DDL。
func TestCrossProcessContractGolden(t *testing.T) {
	root, ok := crossContractBackendGoRoot(t)
	if !ok {
		return
	}
	gatewayRecords := filepath.Join(root, "projects", "gateway", "internal", "gatewayusage", "records.go")
	gatewayOutbox := filepath.Join(root, "projects", "gateway", "cmd", "juhe-ai-gateway", "chain_request_failure_health.go")
	jobsRecords := filepath.Join(root, "projects", "jobs", "internal", "usagewriter", "records.go")
	jobsOutbox := filepath.Join(root, "projects", "jobs", "cmd", "juhe-ai-jobs", "worker_health_probe_outbox.go")

	// gateway 侧属于另一个 module：未检出时跳过（monorepo 完整检出时生效）。
	for _, path := range []string{gatewayRecords, gatewayOutbox} {
		if !crossContractFileExists(path) {
			t.Skipf("gateway 契约源未检出（monorepo 完整检出时本测试生效）: %s", path)
		}
	}
	// jobs 侧属于本 module：缺失即本 module 自身损坏，不是检出范围问题。
	for _, path := range []string{jobsRecords, jobsOutbox} {
		if !crossContractFileExists(path) {
			t.Fatalf("jobs module 自身契约源缺失: %s", path)
		}
	}

	t.Run("UsageRecordInputSpoolJSONShape", func(t *testing.T) {
		gateway := crossContractStructFieldShapes(t, gatewayRecords, "UsageRecordInput")
		jobsSide := crossContractStructFieldShapes(t, jobsRecords, "UsageRecordInput")
		crossContractAssertSameFields(t, gatewayRecords, gateway, jobsRecords, jobsSide)
	})

	t.Run("HealthProbeOutboxDDL", func(t *testing.T) {
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
	})
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

// crossContractFieldShape 是契约字段的 golden 形状：字段名 + 类型文本 +
// struct tag 原文（含定界反引号；两侧源文件的 json tag 原文一致即落盘格式
// 一致；类型一并对比——string ↔ *string 漂移会改变 JSON 的 null 与省略
// 形状，tag 相同也不算一致）。
type crossContractFieldShape struct {
	Name string
	Type string
	Tag  string
}

// crossContractStructFieldShapes 提取指定 struct 类型声明的字段形状序列
// （保持源码声明顺序；多名字段按名字逐个展开）。
func crossContractStructFieldShapes(t *testing.T, path, typeName string) []crossContractFieldShape {
	t.Helper()
	file := crossContractParse(t, path)
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s: %s 不是 struct 类型声明", path, typeName)
			}
			shapes := make([]crossContractFieldShape, 0, len(st.Fields.List))
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					t.Fatalf("%s: %s 字段序列含匿名字段，契约要求全部为命名字段", path, typeName)
				}
				tag := ""
				if field.Tag != nil {
					tag = field.Tag.Value
				}
				for _, name := range field.Names {
					shapes = append(shapes, crossContractFieldShape{Name: name.Name, Type: types.ExprString(field.Type), Tag: tag})
				}
			}
			return shapes
		}
	}
	t.Fatalf("%s: 未找到类型 %s 的声明", path, typeName)
	return nil
}

// crossContractAssertSameFields 断言两侧字段序列完全一致（数量、顺序、
// tag 原文），失败时输出第一个差异位置（第 N 个字段、两侧值）。
func crossContractAssertSameFields(t *testing.T, gatewayPath string, gateway []crossContractFieldShape, jobsPath string, jobsSide []crossContractFieldShape) {
	t.Helper()
	count := len(gateway)
	if len(jobsSide) > count {
		count = len(jobsSide)
	}
	for i := 0; i < count; i++ {
		var haveGateway, haveJobs bool
		var gwShape, jbShape crossContractFieldShape
		if i < len(gateway) {
			haveGateway, gwShape = true, gateway[i]
		}
		if i < len(jobsSide) {
			haveJobs, jbShape = true, jobsSide[i]
		}
		if haveGateway && haveJobs && gwShape == jbShape {
			continue
		}
		t.Fatalf("契约 1（usage-record-spool JSON 记录形状）两侧 UsageRecordInput 第 %d 个字段不一致（gateway 共 %d 个字段，jobs 共 %d 个字段）:\n  gateway %s: %s\n  jobs    %s: %s\n两侧为手工同步拷贝、无编译期联动：写侧新增/改名字段会在读侧静默丢失（极端形状进 .corrupt 隔离），必须同一次提交内同步两侧。",
			i+1, len(gateway), len(jobsSide),
			gatewayPath, crossContractFieldDisplay(haveGateway, gwShape),
			jobsPath, crossContractFieldDisplay(haveJobs, jbShape))
	}
}

func crossContractFieldDisplay(present bool, shape crossContractFieldShape) string {
	if !present {
		return "<缺失（该侧没有这个位置的字段）>"
	}
	return fmt.Sprintf("{Name: %s, Type: %s, Tag: %s}", shape.Name, shape.Type, shape.Tag)
}

// crossContractConstString 提取包级字符串常量的字面量值（strconv.Unquote
// 后的纯文本；两侧契约常量必须保持显式字符串字面量）。
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
