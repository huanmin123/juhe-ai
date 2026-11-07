package accounts

// 账号模型别名 family pair 合法集合的 golden 一致性断言（BUG-0342 根因治理
// T103）。同一份合法 pair 清单存在于四方并曾各自演化（BUG-0342 即写侧
// entryMappingPairTargetModes 漏了 responses->responses 而设计文档 §5.2、
// 前端 protocolRules 与运行时 source==upstream 分支三方放行），本文件把
// testdata/entry_mapping_pairs.json golden 与写侧矩阵、运行时放行逻辑锁成
// 自动断言；设计文档 §5.2 表格与前端侧由 golden 的来源标注（designDocRow /
// frontendRuleSets）与前端回归脚本
// frontend/src/scripts/regression/account-model-mapping-model-options-regression.ts
// 守护。矩阵演化必须先改 golden 再同步四方，任何一方单独漂移都会转红。

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

type entryMappingPairsGolden struct {
	Header      []string          `json:"$header"`
	Authorities map[string]string `json:"authorities"`
	Pairs       []struct {
		Pair             string   `json:"pair"`
		DesignDocRow     int      `json:"designDocRow"`
		TargetModes      []string `json:"targetModes"`
		FrontendRuleSets []string `json:"frontendRuleSets"`
		Note             string   `json:"note,omitempty"`
	} `json:"pairs"`
}

func loadEntryMappingPairsGolden(t *testing.T) entryMappingPairsGolden {
	t.Helper()
	// go test 的工作目录是包源码目录，testdata 相对路径即仓库内固定位置。
	goldenPath := "testdata/entry_mapping_pairs.json"
	content, readErr := os.ReadFile(goldenPath)
	if readErr != nil {
		t.Fatalf("读取 golden %s 失败：%v", goldenPath, readErr)
	}
	var decoded entryMappingPairsGolden
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("解析 golden %s 失败：%v", goldenPath, err)
	}
	return decoded
}

// goldenPairKey 把 golden 的 "source -> target" 字符串还原为写侧矩阵的
// source\x00target 键形态。
func goldenPairKey(pair string) (string, string, error) {
	source, target, found := strings.Cut(pair, " -> ")
	if !found || source == "" || target == "" {
		return "", "", fmt.Errorf("pair %q 不符合 \"source -> target\" 形态", pair)
	}
	return source, target, nil
}

// TestEntryMappingPairsGoldenMatchesWriteMatrix 断言 golden 与写侧唯一实现
// entryMappingPairTargetModes 完全相等：pair 集合双向无差、每对 targetModes
// 集合完全相等（多出/缺失/形态拼写不一致都转红）。
func TestEntryMappingPairsGoldenMatchesWriteMatrix(t *testing.T) {
	golden := loadEntryMappingPairsGolden(t)
	if len(golden.Pairs) == 0 {
		t.Fatal("golden pairs 为空：golden 文件被清空或路径错误")
	}

	type goldenRow struct {
		source string
		target string
		modes  []string
	}
	rows := make([]goldenRow, 0, len(golden.Pairs))
	seen := make(map[string]bool, len(golden.Pairs))
	for _, item := range golden.Pairs {
		source, target, err := goldenPairKey(item.Pair)
		if err != nil {
			t.Fatalf("golden 第 %d 行：%v", item.DesignDocRow, err)
		}
		if seen[item.Pair] {
			t.Fatalf("golden pair 重复：%s", item.Pair)
		}
		seen[item.Pair] = true
		if len(item.TargetModes) == 0 {
			t.Fatalf("golden pair %s 的 targetModes 为空", item.Pair)
		}
		rows = append(rows, goldenRow{source: source, target: target, modes: item.TargetModes})
	}

	// 协议等价判定不变量：7 pair 矩阵内全部 pair 必须满足同族改名或 Gemini
	// stream 归一（2026-11-07 跨协议映射退役）。矩阵若重新引入跨族 pair 在此转红。
	for _, row := range rows {
		if !isProtocolEquivalentEntryMappingFamily(row.source, row.target) {
			t.Fatalf("golden pair %s -> %s 不满足协议等价判定 isProtocolEquivalentEntryMappingFamily（矩阵只允许同族改名与 Gemini stream 归一）", row.source, row.target)
		}
	}

	// 正向：golden 每对必须在写侧矩阵中且 modes 集合完全相等。
	for _, row := range rows {
		key := row.source + "\x00" + row.target
		matrixModes, ok := entryMappingPairTargetModes[key]
		if !ok {
			t.Fatalf("golden pair %s -> %s 在写侧 entryMappingPairTargetModes 缺失（写侧漏项，BUG-0342 同型回潮）", row.source, row.target)
		}
		goldenModes := slices.Clone(row.modes)
		matrixSorted := slices.Clone(matrixModes)
		slices.Sort(goldenModes)
		slices.Sort(matrixSorted)
		if !slices.Equal(goldenModes, matrixSorted) {
			goldenOnly := []string{}
			matrixOnly := []string{}
			for _, mode := range goldenModes {
				if !slices.Contains(matrixSorted, mode) {
					goldenOnly = append(goldenOnly, mode)
				}
			}
			for _, mode := range matrixSorted {
				if !slices.Contains(goldenModes, mode) {
					matrixOnly = append(matrixOnly, mode)
				}
			}
			t.Fatalf("golden pair %s -> %s 的 targetModes 与写侧矩阵不一致：golden 多出 %v，写侧多出 %v（golden %v vs 写侧 %v）",
				row.source, row.target, goldenOnly, matrixOnly, goldenModes, matrixSorted)
		}
	}

	// 反向：写侧矩阵每对必须在 golden 中（写侧单方面新增 pair 也算漂移）。
	for key := range entryMappingPairTargetModes {
		source, target, _ := strings.Cut(key, "\x00")
		if !seen[source+" -> "+target] {
			t.Fatalf("写侧 entryMappingPairTargetModes 的 %s -> %s 不在 golden 中（写侧或 golden 单方面漂移）", source, target)
		}
	}
}

// TestEntryMappingPairsGoldenMatchesRuntimeAdmissibility 断言运行时
// IsAdmissibleModelMapping 与 golden 矩阵一致：
//
//  1. golden 每对（全部是协议等价 pair）运行时都放行；
//  2. 以 golden 全体 source/target family 穷举全组合，运行时 admissible 的
//     pair 集合恰等于 golden（运行时单方面扩权或缩权都转红）。
func TestEntryMappingPairsGoldenMatchesRuntimeAdmissibility(t *testing.T) {
	golden := loadEntryMappingPairsGolden(t)
	account := &gatewayopenai.RuntimeAccount{}
	admissiblePairs := map[string]bool{}

	for _, item := range golden.Pairs {
		source, target, err := goldenPairKey(item.Pair)
		if err != nil {
			t.Fatalf("golden 第 %d 行：%v", item.DesignDocRow, err)
		}
		admissiblePairs[item.Pair] = gatewayopenai.IsAdmissibleModelMapping(gatewayopenai.AccountModelMapping{
			SourceModel:            "golden-source-alias",
			SourceEndpointFamily:   source,
			UpstreamModel:          "golden-upstream-model",
			UpstreamEndpointFamily: target,
		}, account)
		if !admissiblePairs[item.Pair] {
			t.Fatalf("golden pair %s -> %s 运行时 IsAdmissibleModelMapping=false（运行时矩阵缩权或漏放行，BUG-0342 同型回潮）", source, target)
		}
	}

	// 穷举 family 全组合锁定运行时与 golden 的关系边界。已知结构差异（非
	// BUG-0342 同型漂移）：运行时 source==upstream 分支对源族可接受集内任意
	// 同族对无条件放行（如 stream_generate_content -> stream_generate_content），
	// 而该 pair 不在 §5.2 可保存矩阵（写侧 400 拒绝，用户链路不可达）——同族
	// 方向只断言 golden ⊆ 运行时；跨族方向（矩阵漂移的实际发生面）做双向
	// 全等：运行时新增 case（扩权）或删除 case（缩权）都转红。
	families := map[string]bool{}
	for _, item := range golden.Pairs {
		source, target, _ := goldenPairKey(item.Pair)
		families[source] = true
		families[target] = true
	}
	for source := range families {
		for target := range families {
			pair := source + " -> " + target
			supported := gatewayopenai.IsAdmissibleModelMapping(gatewayopenai.AccountModelMapping{
				SourceModel:            "golden-source-alias",
				SourceEndpointFamily:   source,
				UpstreamModel:          "golden-upstream-model",
				UpstreamEndpointFamily: target,
			}, account)
			if source == target {
				if admissiblePairs[pair] && !supported {
					t.Fatalf("golden 同族 pair %s -> %s 运行时拒绝（source==upstream 同族改名放行分支缩权）", source, target)
				}
				continue
			}
			if supported && !admissiblePairs[pair] {
				t.Fatalf("运行时放行了 golden 之外的跨族 pair %s -> %s（运行时矩阵扩权）", source, target)
			}
			if !supported && admissiblePairs[pair] {
				t.Fatalf("golden pair %s -> %s 运行时拒绝（运行时矩阵缩权）", source, target)
			}
		}
	}
}
