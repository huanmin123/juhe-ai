package modelcheckprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

func TestJensenShannonDivergenceKnownValues(t *testing.T) {
	identical := JensenShannonDivergence(map[string]int{"正面": 3, "反面": 3}, map[string]int{"正面": 3, "反面": 3})
	if identical != 0 {
		t.Fatalf("同分布 JSD=%v want 0", identical)
	}
	disjoint := JensenShannonDivergence(map[string]int{"a": 6}, map[string]int{"b": 6})
	if disjoint != 1 {
		t.Fatalf("完全不相交 JSD=%v want 1", disjoint)
	}
	half := JensenShannonDivergence(map[string]int{"a": 3, "b": 3}, map[string]int{"a": 3, "c": 3})
	if half < 0.4999 || half > 0.5001 {
		t.Fatalf("已知分布对 JSD=%v want 0.5", half)
	}
	empty := JensenShannonDivergence(map[string]int{}, map[string]int{})
	if empty != 0 {
		t.Fatalf("双侧空计数 JSD=%v want 0", empty)
	}
	oneSided := JensenShannonDivergence(map[string]int{"a": 6}, map[string]int{})
	if oneSided != 1 {
		t.Fatalf("单侧空计数 JSD=%v want 1", oneSided)
	}
}

func TestExactBinomialTwoSidedKnownValues(t *testing.T) {
	// Bin(8,0.5)：P(X≤0)=1/256，双侧 p=2/256=0.0078125。
	if p := ExactBinomialTwoSided(0, 8); p < 0.00781 || p > 0.00782 {
		t.Fatalf("0/8 p=%v want 0.0078125", p)
	}
	// 3/8：P(X≤3)=93/256，双侧 p=2*93/256=0.7265625。
	if p := ExactBinomialTwoSided(3, 8); p < 0.7265 || p > 0.7266 {
		t.Fatalf("3/8 p=%v want 0.7265625", p)
	}
	// 4/8（对称中点）：p 截断为 1。
	if p := ExactBinomialTwoSided(4, 8); p != 1 {
		t.Fatalf("4/8 p=%v want 1（截断）", p)
	}
	if p := ExactBinomialTwoSided(2, 0); p != 1 {
		t.Fatalf("零试验 p=%v want 1", p)
	}
}

func TestNormalizeSamplingAnswerBuckets(t *testing.T) {
	units := map[string]samplingUnitDefinition{}
	for _, unit := range samplingUnitDefinitions {
		units[unit.key] = unit
	}
	cases := []struct {
		unit, output, want string
	}{
		{"coin", "正面。", "正面"},
		{"coin", "Heads", "正面"},
		{"coin", "反面", "反面"},
		{"coin", "立起来了", samplingOtherBucket},
		{"color", "蓝色", "蓝"},
		{"color", "Red", "红"},
		{"color", "青绿色", samplingOtherBucket},
		{"city", "巴黎", "巴黎"},
		{"city", "成都市", "成都"},
		{"city", "亚特兰蒂斯", samplingOtherBucket},
		{"random_number", "42", "42"},
		{"random_number", "1,000", samplingOtherBucket},
		{"random_number", "没有数字", samplingOtherBucket},
	}
	for _, item := range cases {
		if got := normalizeSamplingAnswer(units[item.unit], item.output); got != item.want {
			t.Fatalf("normalize(%s,%q)=%q want %q", item.unit, item.output, got, item.want)
		}
	}
}

func samplingObservation(key string, target, comparison map[string]int) SamplingUnitObservation {
	observation := SamplingUnitObservation{Key: key, TargetBuckets: target}
	if comparison != nil {
		observation.ComparisonBuckets = comparison
	}
	return observation
}

func TestEvaluateSamplingStatisticsNoComparisonEvidenceOnly(t *testing.T) {
	observations := SamplingObservations{
		TokenUnits: []SamplingUnitObservation{
			samplingObservation("random_number", map[string]int{"42": 4, "7": 2}, nil),
			samplingObservation("color", map[string]int{"蓝": 6}, nil),
			samplingObservation("city", map[string]int{"巴黎": 6}, nil),
			samplingObservation("coin", map[string]int{"正面": 4, "反面": 2}, nil),
		},
		ConsistencyTarget: []string{"1340", "1340", "1340", "1340", "1340", "1340"},
	}
	item := EvaluateSamplingStatistics(observations)
	if item.Kind != "sampling_statistics" || item.Status != "passed" || item.Score != 0 || item.MaxScore != 0 {
		t.Fatalf("无对照证据性通过: %+v", item)
	}
	distribution := item.Evidence["tokenDistribution"].(map[string]any)
	if distribution["verdict"] != "evidence_only" {
		t.Fatalf("无对照不判分: %+v", distribution)
	}
	if _, ok := item.Evidence["reasonCodes"].([]string); !ok {
		t.Fatalf("reasonCodes 类型=%T", item.Evidence["reasonCodes"])
	}
	if len(item.Evidence["reasonCodes"].([]string)) != 0 {
		t.Fatalf("无短路码: %+v", item.Evidence["reasonCodes"])
	}
	paired := item.Evidence["pairedDivergence"].(map[string]any)
	if paired["verdict"] != "evidence_only" {
		t.Fatalf("无对照配对检验仅记证据: %+v", paired)
	}
}

// 2026-10-02 复审判分降级回归：token_distribution 恒为 evidence_only。
// 蒙特卡洛定案——6 采样经验桶估计器下诚实同源账户对 E[avgJSD]≈0.39
// （p99=0.60），任何阈值都不可判分；JSD 只作为证据统计量落盘。
func TestEvaluateSamplingStatisticsDistributionEvidenceOnly(t *testing.T) {
	t.Run("高 JSD 不再判 failed", func(t *testing.T) {
		observations := SamplingObservations{
			ComparisonAttached: true,
			TokenUnits: []SamplingUnitObservation{
				samplingObservation("random_number", map[string]int{"42": 6}, map[string]int{"13": 6}),
				samplingObservation("color", map[string]int{"蓝": 6}, map[string]int{"红": 6}),
				samplingObservation("city", map[string]int{"巴黎": 6}, map[string]int{"伦敦": 6}),
				samplingObservation("coin", map[string]int{"正面": 3, "反面": 3}, map[string]int{"正面": 3, "反面": 3}),
			},
			ConsistencyTarget:     []string{"1340", "1340", "1340", "1340", "1340", "1340"},
			ConsistencyComparison: []string{"1340", "1340", "1340", "1340", "1340", "1340"},
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("判分已降级，高 JSD 不得判 failed: %+v", item)
		}
		if codes := item.Evidence["reasonCodes"].([]string); len(codes) != 0 {
			t.Fatalf("不产生 distribution reasonCode: %+v", codes)
		}
		distribution := item.Evidence["tokenDistribution"].(map[string]any)
		if distribution["verdict"] != "evidence_only" {
			t.Fatalf("verdict=%v want evidence_only: %+v", distribution["verdict"], distribution)
		}
		if average, ok := distribution["averageJsd"].(float64); !ok || average <= 0.7 {
			t.Fatalf("不相交分布的 JSD 均值仍应落证据: %+v", distribution["averageJsd"])
		}
	})
	t.Run("原警戒带区间同样仅记证据", func(t *testing.T) {
		// 逐单元 JSD({6,0},{3,3})≈0.311，均值落在原 0.25-0.35 警戒带。
		observations := SamplingObservations{
			ComparisonAttached: true,
			TokenUnits: []SamplingUnitObservation{
				samplingObservation("coin", map[string]int{"正面": 6, "反面": 0}, map[string]int{"正面": 3, "反面": 3}),
				samplingObservation("color", map[string]int{"蓝": 6, "红": 0}, map[string]int{"蓝": 3, "红": 3}),
				samplingObservation("city", map[string]int{"巴黎": 6, "伦敦": 0}, map[string]int{"巴黎": 3, "伦敦": 3}),
				samplingObservation("random_number", map[string]int{"42": 6, "7": 0}, map[string]int{"42": 3, "7": 3}),
			},
			ConsistencyTarget:     []string{"1340", "1340", "1340", "1340", "1340", "1340"},
			ConsistencyComparison: []string{"1340", "1340", "1340", "1340", "1340", "1340"},
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("警戒带不再 warning: %+v", item)
		}
		if codes := item.Evidence["reasonCodes"].([]string); len(codes) != 0 {
			t.Fatalf("不产生 distribution reasonCode: %+v", codes)
		}
		distribution := item.Evidence["tokenDistribution"].(map[string]any)
		if distribution["verdict"] != "evidence_only" {
			t.Fatalf("verdict=%v want evidence_only: %+v", distribution["verdict"], distribution)
		}
	})
	t.Run("单侧空桶单元标 insufficient 不计均值", func(t *testing.T) {
		observations := SamplingObservations{
			ComparisonAttached: true,
			TokenUnits: []SamplingUnitObservation{
				samplingObservation("coin", map[string]int{"正面": 3, "反面": 3}, map[string]int{"正面": 3, "反面": 3}),
				samplingObservation("color", map[string]int{"蓝": 6}, map[string]int{"红": 6}),
				samplingObservation("city", map[string]int{"巴黎": 6}, map[string]int{}),
				samplingObservation("random_number", map[string]int{"42": 6}, nil),
			},
			ConsistencyTarget:     []string{"1340", "1340", "1340", "1340", "1340", "1340"},
			ConsistencyComparison: []string{"1340", "1340", "1340", "1340", "1340", "1340"},
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("证据性项不得判 failed: %+v", item)
		}
		distribution := item.Evidence["tokenDistribution"].(map[string]any)
		units := distribution["units"].([]map[string]any)
		if _, ok := units[0]["insufficient"]; ok {
			t.Fatalf("双侧均有样本的单元不应标 insufficient: %+v", units[0])
		}
		if _, ok := units[0]["jsd"]; !ok {
			t.Fatalf("双侧均有样本的单元应落 JSD: %+v", units[0])
		}
		if units[2]["insufficient"] != true || units[3]["insufficient"] != true {
			t.Fatalf("单侧空桶单元应标 insufficient: %+v", units)
		}
		if _, ok := units[2]["jsd"]; ok {
			t.Fatalf("单侧空桶单元不应落 JSD: %+v", units[2])
		}
		if average, ok := distribution["averageJsd"].(float64); !ok {
			t.Fatalf("均值只聚合双侧均有样本的单元: %+v", distribution)
		} else {
			// 仅 coin（JSD=0）与 color（JSD=1）参与均值。
			if average != 0.5 {
				t.Fatalf("均值=%v want 0.5（coin 0 + color 1 的平均）", average)
			}
		}
	})
	t.Run("全单元单侧空桶则无 JSD 均值", func(t *testing.T) {
		observations := SamplingObservations{
			ComparisonAttached: true,
			TokenUnits: []SamplingUnitObservation{
				samplingObservation("random_number", map[string]int{"42": 6}, map[string]int{}),
				samplingObservation("color", map[string]int{"蓝": 6}, map[string]int{}),
				samplingObservation("city", map[string]int{"巴黎": 6}, map[string]int{}),
				samplingObservation("coin", map[string]int{"正面": 6}, map[string]int{}),
			},
			ConsistencyTarget:     []string{"1340", "1340", "1340", "1340", "1340", "1340"},
			ConsistencyComparison: []string{"1340", "1340", "1340", "1340", "1340", "1340"},
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("证据性项不得判 failed: %+v", item)
		}
		distribution := item.Evidence["tokenDistribution"].(map[string]any)
		if _, ok := distribution["averageJsd"]; ok {
			t.Fatalf("全单元不足不得输出 JSD 均值: %+v", distribution)
		}
		if distribution["verdict"] != "evidence_only" {
			t.Fatalf("verdict=%v want evidence_only: %+v", distribution["verdict"], distribution)
		}
	})
}

func TestEvaluateSamplingStatisticsConsistencyNumericWordingSingleCluster(t *testing.T) {
	// 数值归一：「答案1340」「1340」「1,340」是同一答案的三种措辞，必须归
	// 同簇；否则锚点数值题的措辞差异会被误报 mixing_detected。
	observations := SamplingObservations{
		TokenUnits: []SamplingUnitObservation{
			samplingObservation("coin", map[string]int{"正面": 3, "反面": 3}, nil),
		},
		ConsistencyTarget: []string{"答案1340", "1340", "1,340", "总数为1，340", "答案 1340", "1340"},
	}
	item := EvaluateSamplingStatistics(observations)
	if item.Status != "passed" || samplingReasonCodesContain(item.Evidence, "mixing_detected") {
		t.Fatalf("数值措辞差异不得误报混用: %+v", item.Evidence["reasonCodes"])
	}
	consistency := item.Evidence["consistencySampling"].(map[string]any)
	if consistency["clusterCount"] != 1 {
		t.Fatalf("三种数值措辞应归单簇: %+v", consistency)
	}
}

func TestEvaluateSamplingStatisticsConsistencyMixing(t *testing.T) {
	observations := SamplingObservations{
		TokenUnits: []SamplingUnitObservation{
			samplingObservation("coin", map[string]int{"正面": 3, "反面": 3}, nil),
		},
		ConsistencyTarget: []string{"1340", "1340", "1340", "1340", "999", "999"},
	}
	item := EvaluateSamplingStatistics(observations)
	if item.Status != "failed" || !samplingReasonCodesContain(item.Evidence, "mixing_detected") {
		t.Fatalf("两个互斥答案簇应判混用: %+v", item.Evidence["reasonCodes"])
	}
	consistency := item.Evidence["consistencySampling"].(map[string]any)
	if consistency["clusterCount"] != 2 {
		t.Fatalf("簇数=%v", consistency["clusterCount"])
	}
}

func TestEvaluateSamplingStatisticsComparisonAnomalyRecorded(t *testing.T) {
	observations := SamplingObservations{
		ComparisonAttached: true,
		TokenUnits: []SamplingUnitObservation{
			samplingObservation("coin", map[string]int{"正面": 3, "反面": 3}, map[string]int{"正面": 3, "反面": 3}),
		},
		ConsistencyTarget:     []string{"1340", "1340", "1340", "1340", "1340", "1340"},
		ConsistencyComparison: []string{"1340", "1340", "999", "999", "42", "42"},
	}
	item := EvaluateSamplingStatistics(observations)
	if item.Status != "passed" {
		t.Fatalf("对照分裂而目标一致按 passed: %+v", item)
	}
	consistency := item.Evidence["consistencySampling"].(map[string]any)
	if consistency["comparisonAnomaly"] != true || consistency["comparisonClusterCount"] != 3 {
		t.Fatalf("对照异常记录=%+v", consistency)
	}
}

func TestEvaluateSamplingStatisticsPairedDivergence(t *testing.T) {
	mkPairs := func(targetCorrect ...bool) []SamplingPair {
		pairs := make([]SamplingPair, 0, len(targetCorrect))
		for _, correct := range targetCorrect {
			pairs = append(pairs, SamplingPair{TargetCorrect: correct, ComparisonCorrect: !correct})
		}
		return pairs
	}
	t.Run("显著差于对照判系统性偏离", func(t *testing.T) {
		// b=8, c=0：p=0.0078 < 0.05 且目标系统性更差。
		observations := SamplingObservations{
			TokenUnits:        []SamplingUnitObservation{samplingObservation("coin", map[string]int{"正面": 6}, nil)},
			ConsistencyTarget: []string{"1340"},
			PairedAttached:    true,
			Pairs:             mkPairs(false, false, false, false, false, false, false, false),
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "failed" || !samplingReasonCodesContain(item.Evidence, "systematic_divergence") {
			t.Fatalf("item=%+v reasons=%+v", item, item.Evidence["reasonCodes"])
		}
		paired := item.Evidence["pairedDivergence"].(map[string]any)
		if paired["systematic"] != true || paired["targetWrongComparisonCorrect"] != 8 {
			t.Fatalf("配对证据=%+v", paired)
		}
	})
	t.Run("不显著只记证据", func(t *testing.T) {
		// b=1, c=6：目标更好方向，p 不显著。
		observations := SamplingObservations{
			TokenUnits:        []SamplingUnitObservation{samplingObservation("coin", map[string]int{"正面": 6}, nil)},
			ConsistencyTarget: []string{"1340"},
			PairedAttached:    true,
			Pairs:             mkPairs(true, true, true, true, true, true, false),
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("目标更优不得判 failed: %+v", item)
		}
	})
	t.Run("无对照不判分", func(t *testing.T) {
		observations := SamplingObservations{
			TokenUnits:        []SamplingUnitObservation{samplingObservation("coin", map[string]int{"正面": 6}, nil)},
			ConsistencyTarget: []string{"1340"},
			Pairs:             mkPairs(false, false, false, false, false, false, false, false),
		}
		item := EvaluateSamplingStatistics(observations)
		if item.Status != "passed" {
			t.Fatalf("无对照配对偏离仅记证据: %+v", item)
		}
	})
}

func TestEvaluateSamplingStatisticsAllFailuresSkip(t *testing.T) {
	observations := SamplingObservations{
		TokenUnits: []SamplingUnitObservation{
			{Key: "coin", TargetBuckets: map[string]int{}, TargetFailures: 6},
		},
		ConsistencyTargetFailures: 6,
	}
	item := EvaluateSamplingStatistics(observations)
	if item.Status != "skipped" || item.Evidence["requestFailure"] != true {
		t.Fatalf("全部采样失败不伪造成失败: %+v", item)
	}
}

func TestRunSamplingStatisticsFullSuiteNoComparison(t *testing.T) {
	transport := &wlEchoTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:                  "https://sampling.example",
		Client:                    &http.Client{Transport: transport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     "gpt-5.6-sol",
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
		ModelLimits:               deterministicLimits{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var sampling Evaluation
	for _, item := range items {
		if item.Kind == "sampling_statistics" {
			sampling = item
		}
	}
	if sampling.Kind == "" || sampling.Status != "passed" {
		t.Fatalf("sampling=%+v items=%+v", sampling, items)
	}
	distribution := sampling.Evidence["tokenDistribution"].(map[string]any)
	if distribution["verdict"] != "evidence_only" {
		t.Fatalf("无对照仅落分布证据: %+v", distribution)
	}
	units := distribution["units"].([]map[string]any)
	if len(units) != 4 {
		t.Fatalf("4 个采样单元: %+v", units)
	}
	for _, unit := range units {
		if unit["sampleCount"] != 6 {
			t.Fatalf("每单元 6 采样: %+v", unit)
		}
	}
	consistency := sampling.Evidence["consistencySampling"].(map[string]any)
	if consistency["clusterCount"] != 1 {
		t.Fatalf("echo 上游锚点作答应单簇: %+v", consistency)
	}
}

func TestRunSamplingStatisticsWithComparisonInterleaved(t *testing.T) {
	targetTransport := &wlEchoTransport{}
	comparisonTransport := &wlEchoTransport{}
	target := Suite{
		Endpoint:                  "https://target.example",
		Client:                    &http.Client{Transport: targetTransport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     "gpt-5.6-sol",
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
		ModelLimits:               deterministicLimits{},
	}
	comparison := target
	comparison.Endpoint = "https://comparison.example"
	comparison.Client = &http.Client{Transport: comparisonTransport}
	comparison.Model = "gpt-5.6-terra"
	target.Comparison = &comparison
	items, err := RunSuite(context.Background(), target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var sampling, nestedSkip Evaluation
	for _, item := range items {
		switch item.Kind {
		case "sampling_statistics":
			sampling = item
		case "trusted_comparison.sampling_statistics":
			nestedSkip = item
		}
	}
	if sampling.Kind == "" || sampling.Status != "passed" {
		t.Fatalf("sampling=%+v", sampling)
	}
	if sampling.Evidence["comparisonAttached"] != true {
		t.Fatalf("对照采样必须执行: %+v", sampling.Evidence)
	}
	distribution := sampling.Evidence["tokenDistribution"].(map[string]any)
	if distribution["verdict"] != "evidence_only" {
		t.Fatalf("token_distribution 判分已降级，同分布也不判 passed: %+v", distribution)
	}
	if nestedSkip.Kind == "" || nestedSkip.Status != "skipped" || nestedSkip.Evidence["reason"] != "trusted_comparison_not_attached" {
		t.Fatalf("嵌套对照套件必须构造性跳过采样族: %+v", nestedSkip)
	}
	paired := sampling.Evidence["pairedDivergence"].(map[string]any)
	if paired["verdict"] != "passed" {
		t.Fatalf("echo 双方一致无系统性偏离: %+v", paired)
	}
	// 采样族请求：目标 24+6，对照 24+6（交错串行）。
	if len(targetTransport.requests) < 30 || len(comparisonTransport.requests) < 30 {
		t.Fatalf("采样请求不足: target=%d comparison=%d", len(targetTransport.requests), len(comparisonTransport.requests))
	}
}

func TestRunSuiteQuickHasIdentityButNoSampling(t *testing.T) {
	transport := &wlEchoTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:                  "https://quick.example",
		Client:                    &http.Client{Transport: transport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     "gpt-5.6-sol",
		Profile:                   "quick",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Evaluation{}
	for _, item := range items {
		kinds[item.Kind] = item
	}
	for _, kind := range []string{"identity_selfreport", "identity_extraction", "identity_anchor"} {
		item, ok := kinds[kind]
		if !ok {
			t.Fatalf("quick 缺 %s: %+v", kind, items)
		}
		if item.Status != "passed" {
			t.Fatalf("%s 应通过: %+v", kind, item)
		}
	}
	if _, ok := kinds["sampling_statistics"]; ok {
		t.Fatalf("quick 不执行采样族: %+v", items)
	}
}

func TestRunSuiteCoreFailureSkipsIdentityAndSampling(t *testing.T) {
	transport := &coreFailureTransport{}
	items, err := RunSuite(context.Background(), Suite{
		Endpoint:    "https://dead.example",
		Client:      &http.Client{Transport: transport},
		Model:       "gpt-5.6-sol",
		Profile:     "full",
		Protocol:    modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:   deterministicTokenizer{},
		ModelLimits: deterministicLimits{},
		Retry:       RetryOptions{AttemptTimeouts: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, Delay: func(context.Context) error { return nil }},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		switch unscopedKind(item.Kind) {
		case "identity_selfreport", "identity_extraction", "identity_anchor", "sampling_statistics":
			t.Fatalf("核心失败截断后不得执行新家族: %+v", items)
		}
	}
}

func TestSamplingPairsAnchorRequiresSameQuestion(t *testing.T) {
	capture := newProbeCapture()
	capture.targetAnchor = &anchorObservation{question: "锚点题A", correct: true}
	capture.comparisonAnchor = &anchorObservation{question: "锚点题B", correct: false}
	if pairs := capture.samplingPairs(); len(pairs) != 0 {
		t.Fatalf("题目不同侧不得配对: %+v", pairs)
	}
	capture.comparisonAnchor = &anchorObservation{question: "锚点题A", correct: false}
	pairs := capture.samplingPairs()
	if len(pairs) != 1 || !pairs[0].TargetCorrect || pairs[0].ComparisonCorrect {
		t.Fatalf("同题侧必须配对: %+v", pairs)
	}
}

// anchorQuestionFromRequests 提取 transport 收到的锚点题题干（含参数）。
func anchorQuestionFromRequests(t *testing.T, requests []string) string {
	t.Helper()
	for _, body := range requests {
		if index := strings.Index(body, "仓库清点锚点题"); index >= 0 {
			question := body[index:]
			if end := strings.IndexByte(question, '"'); end > 0 {
				return question[:end]
			}
		}
	}
	return ""
}

func TestRunSamplingStatisticsComparisonReusesTargetAnchorQuestion(t *testing.T) {
	targetTransport := &wlEchoTransport{}
	comparisonTransport := &wlEchoTransport{}
	target := Suite{
		Endpoint:                  "https://anchor-target.example",
		Client:                    &http.Client{Transport: targetTransport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     "gpt-5.6-sol",
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
		ModelLimits:               deterministicLimits{},
	}
	comparison := target
	comparison.Endpoint = "https://anchor-comparison.example"
	comparison.Client = &http.Client{Transport: comparisonTransport}
	comparison.Model = "gpt-5.6-terra"
	target.Comparison = &comparison
	items, err := RunSuite(context.Background(), target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var sampling Evaluation
	for _, item := range items {
		if item.Kind == "sampling_statistics" {
			sampling = item
		}
	}
	if sampling.Kind == "" {
		t.Fatalf("缺少采样族: %+v", items)
	}
	// 配对题两侧同串：对照收到的锚点题与目标侧完全一致（同题同参数）。
	targetQuestion := anchorQuestionFromRequests(t, targetTransport.requests)
	comparisonQuestion := anchorQuestionFromRequests(t, comparisonTransport.requests)
	if targetQuestion == "" || targetQuestion != comparisonQuestion {
		t.Fatalf("锚点配对必须同题同参数: target=%q comparison=%q", targetQuestion, comparisonQuestion)
	}
	paired := sampling.Evidence["pairedDivergence"].(map[string]any)
	if paired["verdict"] != "passed" {
		t.Fatalf("echo 双方一致无系统性偏离: %+v", paired)
	}
	if pairCount, ok := paired["pairCount"].(int); !ok || pairCount < 1 {
		t.Fatalf("同题锚点必须进入 McNemar 配对表: %+v", paired)
	}
}

// randomSamplingTransport 模拟诚实同源账户的同分布独立随机采样：token_
// distribution 的采样请求按单元桶分布独立随机作答（固定种子确定性伪随机，
// 非固定回声），其余探针沿用 wlEchoTransport 的回声契约（锚点题与
// temperature=0 一致性轮次保持确定性正解）。
type randomSamplingTransport struct {
	wlEchoTransport
	rng *rand.Rand
}

func (t *randomSamplingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	text := string(body)
	if answer, ok := t.randomBucketAnswer(text); ok {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		t.mu.Lock()
		t.requests = append(t.requests, text)
		t.mu.Unlock()
		encoded, _ := json.Marshal(answer)
		responseBody := fmt.Sprintf(`{"model":%q,"output_text":%s,"usage":{"input_tokens":10,"total_tokens":11}}`, payload.Model, encoded)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return t.wlEchoTransport.RoundTrip(request)
}

// randomBucketAnswer 命中采样单元问法时按该单元的桶分布独立随机作答。
func (t *randomSamplingTransport) randomBucketAnswer(text string) (string, bool) {
	for _, unit := range samplingUnitDefinitions {
		matched := false
		for _, variant := range unit.variants {
			if strings.Contains(text, variant) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if unit.numeric {
			return strconv.Itoa(1 + t.rng.Intn(100)), true
		}
		if len(unit.vocab) > 0 {
			return unit.vocab[t.rng.Intn(len(unit.vocab))], true
		}
	}
	return "", false
}

// CI 盲区回归（Blocker 1）：两个诚实同源账户按各自桶分布独立随机采样时，
// 6 样本经验桶估计器的 JSD 均值天然落在远超已废弃 0.35 阈值的区间；判分
// 降级为 evidence_only 后，这类账户不得产生任何 reasonCode。固定回声 mock
// （双侧逐字相同 → JSD=0）测不出该路径。
func TestRunSamplingStatisticsSameDistributionRandomSamplingNoReasonCodes(t *testing.T) {
	targetTransport := &randomSamplingTransport{rng: rand.New(rand.NewSource(101))}
	comparisonTransport := &randomSamplingTransport{rng: rand.New(rand.NewSource(202))}
	target := Suite{
		Endpoint:                  "https://random-target.example",
		Client:                    &http.Client{Transport: targetTransport},
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		Model:                     "gpt-5.6-sol",
		Profile:                   "full",
		Protocol:                  modelcheckprofile.ProtocolOpenAIResponses,
		Tokenizer:                 deterministicTokenizer{},
		ModelLimits:               deterministicLimits{},
	}
	comparison := target
	comparison.Endpoint = "https://random-comparison.example"
	comparison.Client = &http.Client{Transport: comparisonTransport}
	comparison.Model = "gpt-5.6-terra"
	target.Comparison = &comparison
	items, err := RunSuite(context.Background(), target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var sampling Evaluation
	for _, item := range items {
		if item.Kind == "sampling_statistics" {
			sampling = item
		}
	}
	if sampling.Kind == "" || sampling.Status != "passed" {
		t.Fatalf("同分布独立随机采样不得判 failed: %+v", sampling)
	}
	if codes := sampling.Evidence["reasonCodes"].([]string); len(codes) != 0 {
		t.Fatalf("同分布独立随机采样不得产生任何 reasonCode: %+v", codes)
	}
	distribution := sampling.Evidence["tokenDistribution"].(map[string]any)
	if distribution["verdict"] != "evidence_only" {
		t.Fatalf("verdict=%v want evidence_only: %+v", distribution["verdict"], distribution)
	}
	// 断言 JSD 均值超过已废弃阈值，证明本用例确实覆盖旧判分路径的盲区。
	average, ok := distribution["averageJsd"].(float64)
	if !ok || average <= 0.35 {
		t.Fatalf("独立随机采样的 JSD 均值应超过已废弃的 0.35 阈值: %+v", distribution["averageJsd"])
	}
}
