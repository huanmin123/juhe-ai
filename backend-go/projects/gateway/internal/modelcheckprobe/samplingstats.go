package modelcheckprobe

// §17 full 采样统计族（sampling_statistics 家族）。
//
// 依据 docs/functions/模型检测设计.md §17：单轮探针测不出概率性混用、同族
// 近亲降级与解码参数漂移，必须靠采样统计。三个子机制合成一个家族证据项：
//   - token_distribution    4 个单 token 单元 × 6 采样，显式 temperature=1、
//     短 max_tokens；答案归一化（未见答案入 OTHER 桶）为经验分布，有可信
//     对照时计算逐单元 JSD 统计量。2026-10-02 复审判分降级：6 采样经验桶
//     估计器下诚实同源账户对 E[avgJSD]≈0.39（p99=0.60），任何阈值都不可
//     判分，本子机制恒为 evidence_only（不产生 reasonCode），判分待真实
//     同源数据逐单元校准后另行启用；
//   - consistency_sampling  取本轮锚点题同题 temperature=0 重复采样 6 次，
//     答案先做数值归一（首个整数序列为规范形）；≥2 个互斥答案簇 →
//     mixing_detected；有对照时对照同跑，对照分裂而目标一致 → passed 且
//     记录对照异常；
//   - paired_divergence     0 请求，复用本轮行为题+锚点题的目标/对照答案
//     对错格局做 McNemar 精确二项检验（锚点按题目文本配对，对照套件复用
//     目标侧锚点题），p<0.05 且目标系统性差于对照 → systematic_divergence。
//
// 温度可控性边界（2026-10-02 终审 CONCERN-A）：consistency_sampling 的
// temperature=0 同题一致前提仅在温度可实际送达上游的路径成立。两条温度
// 不可控路径——OpenAI OAuth Codex 适配（tunedBasic 不应用温度，且
// normalizeOpenAIOAuthCodexRequest 在协议层 delete temperature）与
// Anthropic 协议（buildBasicWithTunings 只写 max_tokens，不送温度字段）——
// 上的一致性采样运行在默认温度，诚实账户（尤其弱模型）答案天然分裂，不得
// 保留 mixing_detected 硬判。三个子机制的差异化处理：consistency_sampling
// 在温度不可控路径降级 evidence_only（簇结构证据照常落库、不判 mixing_
// detected）；token_distribution 本就 evidence_only（2026-10-02 判分降级）
// 不受温度影响；paired_divergence 不受影响——配对检验对温度不敏感（目标与
// 对照两侧同条件采样，采样噪声由 McNemar 的 p 值处理）。
//
// 评分契约：三项均不进普通分母（MaxScore=0）；mixing_detected /
// systematic_divergence 任一成立由 summary 层整轮短路 suspicious（token_
// distribution 的 distribution_mismatch / distribution_divergent 已随判分
// 降级移除）。统计证据只落桶计数与统计量（JSD、p 值、桶标签 ≤50 字），
// 不落完整响应正文。采样串行错峰、对照与目标交错进行（本套件请求天然
// 串行，无需额外并发控制）。

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

const (
	// samplingUnitCount × samplingSamplesPerUnit = 24 个目标请求（无对照）；
	// 有对照时对照账户并行同机制采样，总量翻倍（§17 预算口径）。
	samplingUnitCount          = 4
	samplingSamplesPerUnit     = 6
	samplingConsistencyRounds  = 6
	samplingBucketLabelLimit   = 50
	samplingDistributionBudget = 16 // token_distribution 的短 max_tokens 预算
	samplingConsistencyBudget  = 64
	samplingOtherBucket        = "OTHER"

	samplingBinomialAlpha = 0.05
)

// samplingUnitDefinition 是一个单 token 采样单元：随机问法池 + 归一化词表。
// numeric 为 true 时（1-100 随机数）任意 1..100 整数本身即合法桶标签。
type samplingUnitDefinition struct {
	key      string
	variants []string
	numeric  bool
	vocab    []string
}

var samplingUnitDefinitions = []samplingUnitDefinition{
	{
		key: "random_number",
		variants: []string{
			"从 1 到 100 中随机选一个整数，只输出这个数字。",
			"请在 1 到 100 之间任选一个随机整数，只输出数字本身。",
			"随机数任务：输出一个 1 到 100 范围内的整数，不要输出其他内容。",
		},
		numeric: true,
	},
	{
		key: "color",
		variants: []string{
			"用一个中文词说出一种颜色，只输出颜色名。",
			"颜色任务：给出一个常见颜色的中文名，只输出颜色本身。",
			"请直接写出一个颜色的中文名称，不要输出其他内容。",
		},
		vocab: []string{"红", "橙", "黄", "绿", "蓝", "紫", "黑", "白", "粉", "棕", "灰", "金", "银"},
	},
	{
		key: "city",
		variants: []string{
			"说出一个世界著名城市的中文名，只输出城市名。",
			"城市任务：给出一个知名城市的中文地名，只输出名称本身。",
			"请直接写出一个著名城市的名字（中文），不要输出其他内容。",
		},
		vocab: []string{"北京", "上海", "广州", "深圳", "香港", "成都", "东京", "首尔", "新加坡", "伦敦", "巴黎", "纽约", "悉尼", "柏林", "罗马", "莫斯科", "迪拜"},
	},
	{
		key: "coin",
		variants: []string{
			"模拟抛一枚均匀硬币的结果，只输出「正面」或「反面」。",
			"硬币任务：随机给出正面或反面，只输出这两个词之一。",
			"请抛一次硬币并只输出结果：正面 或 反面。",
		},
		vocab: []string{"正面", "反面"},
	},
}

var samplingEnglishColorPattern = map[string]string{
	"red": "红", "orange": "橙", "yellow": "黄", "green": "绿", "blue": "蓝",
	"purple": "紫", "violet": "紫", "black": "黑", "white": "白", "pink": "粉",
	"brown": "棕", "gray": "灰", "grey": "灰", "gold": "金", "silver": "银",
}

var samplingIntegerPattern = regexp.MustCompile(`[0-9][0-9,，]*`)

// probeCapture 收集 paired_divergence 需要的本轮逐题对错格局：目标与可信
// 对照账户在行为题与锚点题上的 executed 结果。只在同一次 RunSuite 执行内
// 存活，不落盘。
type probeCapture struct {
	targetBehavior     map[string]bool
	comparisonBehavior map[string]bool
	targetAnchor       *anchorObservation
	comparisonAnchor   *anchorObservation
}

type anchorObservation struct {
	question string
	correct  bool
}

func newProbeCapture() *probeCapture {
	return &probeCapture{targetBehavior: map[string]bool{}, comparisonBehavior: map[string]bool{}}
}

func (c *probeCapture) recordBehaviorSample(prefix string, request Request, result Result) {
	if c == nil {
		return
	}
	prompt := requestUserPrompt(request)
	key := behaviorProbeKeyForPrompt(prompt)
	if key == "" {
		return
	}
	correct := result.Success && behaviorPassed(key, result.Output)
	if strings.TrimSpace(prefix) == "trusted_comparison" {
		c.comparisonBehavior[key] = correct
		return
	}
	c.targetBehavior[key] = correct
}

func (c *probeCapture) recordAnchor(prefix string, probe IdentityAnchorProbe, correct bool) {
	if c == nil {
		return
	}
	observation := &anchorObservation{question: probe.Question, correct: correct}
	if strings.TrimSpace(prefix) == "trusted_comparison" {
		c.comparisonAnchor = observation
		return
	}
	c.targetAnchor = observation
}

// samplingPairs 汇总双方都有 executed 回执的行为题与锚点题对错格局。
// 锚点按题目文本配对（question 是配对键）：对照套件复用目标侧锚点题，题目
// 不同侧不配对，避免把题目难度差异当成模型差异计入 McNemar 不一致对。
func (c *probeCapture) samplingPairs() []SamplingPair {
	if c == nil {
		return nil
	}
	var pairs []SamplingPair
	for key, targetCorrect := range c.targetBehavior {
		comparisonCorrect, ok := c.comparisonBehavior[key]
		if !ok {
			continue
		}
		pairs = append(pairs, SamplingPair{TargetCorrect: targetCorrect, ComparisonCorrect: comparisonCorrect})
	}
	if c.targetAnchor != nil && c.comparisonAnchor != nil && c.targetAnchor.question == c.comparisonAnchor.question {
		pairs = append(pairs, SamplingPair{TargetCorrect: c.targetAnchor.correct, ComparisonCorrect: c.comparisonAnchor.correct})
	}
	return pairs
}

// SamplingPair 是一道复用题的目标/对照对错格局（McNemar 表的一个不一致对来源）。
type SamplingPair struct {
	TargetCorrect, ComparisonCorrect bool
}

// SamplingUnitObservation 是一个采样单元的双侧桶计数。
type SamplingUnitObservation struct {
	Key                string
	TargetBuckets      map[string]int
	ComparisonBuckets  map[string]int
	TargetFailures     int
	ComparisonFailures int
}

// SamplingObservations 是采样族的全部原始观测；EvaluateSamplingStatistics
// 据此纯函数求值，便于测试回放。
type SamplingObservations struct {
	ComparisonAttached            bool
	TokenUnits                    []SamplingUnitObservation
	ConsistencyTarget             []string // 归一化后的成功答案
	ConsistencyComparison         []string
	ConsistencyTargetFailures     int
	ConsistencyComparisonFailures int
	Pairs                         []SamplingPair
	PairedAttached                bool
	// TemperatureUncontrolled 为 true 表示目标侧采样温度无法送达上游
	// （CONCERN-A：Codex 适配或 Anthropic 协议），consistency_sampling 恒
	// 降级 evidence_only。零值 false 保持既有温度可控路径的判分语义。
	TemperatureUncontrolled bool
}

// samplingTemperatureControlled 判定采样温度能否实际送达上游（纯函数）。
// 两条不可控路径（与 tunedBasic 的实际请求构造一致）：
//   - Adapter == AdapterOpenAIOAuthCodex：tunedBasic 走 BuildOpenAIOAuthCodexBasic
//     不应用温度，且 normalizeOpenAIOAuthCodexRequest 在协议层 delete temperature；
//   - Protocol == anthropic：buildBasicWithTunings 对 Anthropic 只写 max_tokens，
//     不送温度字段（§4.2 既定避 400 约束）。
//
// openai_chat / openai_responses 直连与 gemini_native 均显式写 temperature，
// 温度可控。
func samplingTemperatureControlled(adapter string, protocol modelcheckprofile.Protocol) bool {
	return adapter != AdapterOpenAIOAuthCodex && protocol != modelcheckprofile.ProtocolAnthropic
}

// samplingTemperatureReachable 报告本套件（目标侧）的采样温度是否可送达
// 上游；协议解析与 tunedBasic 同源（UpstreamProtocol 优先，回落 Protocol）。
func (s Suite) samplingTemperatureReachable() bool {
	protocol := s.UpstreamProtocol
	if protocol == "" {
		protocol = s.Protocol
	}
	return samplingTemperatureControlled(s.Adapter, protocol)
}

// RunSamplingStatistics 执行 §17 采样统计族并返回家族证据项；第二个返回值
// 表示发生终局请求失败（调用方按家族约定截断整轮）。仅 full 档顶层套件调用；
// 嵌套可信对照套件不执行本族（预算契约：有对照翻倍而非四倍），由 suite.go
// 发构造性 scope 跳过项。
func RunSamplingStatistics(ctx context.Context, input Suite, timeout time.Duration, anchor IdentityAnchorProbe) (Evaluation, bool, error) {
	_, stream, err := input.probeMode()
	if err != nil {
		return Evaluation{}, false, err
	}
	upstreamMode := strings.TrimSpace(input.UpstreamEndpointMode)
	if upstreamMode == "" {
		upstreamMode = modelcheckprofile.EndpointModeForProtocol(input.UpstreamProtocol, stream)
	}
	targetRun, targetTerminal := input.familyRunner(timeout)
	comparison := input.Comparison
	var comparisonRun func(context.Context, Request) (Result, error)
	var comparisonTerminal func() bool
	comparisonMode, comparisonStream := "", false
	if comparison != nil {
		comparisonMode, comparisonStream, err = comparison.probeMode()
		if err != nil {
			return Evaluation{}, false, err
		}
		if comparison.UpstreamEndpointMode != "" {
			comparisonMode = comparison.UpstreamEndpointMode
		}
		comparisonRun, comparisonTerminal = comparison.familyRunner(timeout)
	}
	observations := SamplingObservations{
		ComparisonAttached: comparison != nil,
		PairedAttached:     comparison != nil,
		// CONCERN-A 传导：温度不可控（Codex 适配 / Anthropic 协议）时
		// consistency_sampling 降级 evidence_only，只按目标侧判定（对照侧
		// 分裂本就不产生处罚，仅记 comparisonAnomaly 证据）。
		TemperatureUncontrolled: !input.samplingTemperatureReachable(),
	}

	// token_distribution：串行错峰，目标与对照逐采样交错。
	observations.TokenUnits = make([]SamplingUnitObservation, 0, samplingUnitCount)
	for _, unit := range samplingUnitDefinitions {
		observation := SamplingUnitObservation{Key: unit.key, TargetBuckets: map[string]int{}}
		if comparison != nil {
			observation.ComparisonBuckets = map[string]int{}
		}
		prompt, promptErr := randomIdentityVariant(unit.variants)
		if promptErr != nil {
			return Evaluation{}, false, promptErr
		}
		for sample := 0; sample < samplingSamplesPerUnit; sample++ {
			targetResult, runErr := runSamplingRequest(ctx, input, targetRun, prompt, samplingDistributionBudget, 1.0, upstreamMode, stream)
			if runErr != nil {
				return Evaluation{}, false, runErr
			}
			if targetTerminal() {
				return samplingTerminalEvaluation(targetResult), true, nil
			}
			if targetResult.Success {
				observation.TargetBuckets[normalizeSamplingAnswer(unit, targetResult.Output)]++
			} else {
				observation.TargetFailures++
			}
			if comparisonRun != nil {
				comparisonResult, comparisonErr := runSamplingRequest(ctx, *comparison, comparisonRun, prompt, samplingDistributionBudget, 1.0, comparisonMode, comparisonStream)
				if comparisonErr != nil {
					return Evaluation{}, false, comparisonErr
				}
				if comparisonTerminal() {
					return samplingTerminalEvaluation(comparisonResult), true, nil
				}
				if comparisonResult.Success {
					observation.ComparisonBuckets[normalizeSamplingAnswer(unit, comparisonResult.Output)]++
				} else {
					observation.ComparisonFailures++
				}
			}
		}
		observations.TokenUnits = append(observations.TokenUnits, observation)
	}

	// consistency_sampling：本轮锚点题同题 temperature=0 重复采样。
	for round := 0; round < samplingConsistencyRounds; round++ {
		targetResult, runErr := runSamplingRequest(ctx, input, targetRun, anchor.Question, samplingConsistencyBudget, 0, upstreamMode, stream)
		if runErr != nil {
			return Evaluation{}, false, runErr
		}
		if targetTerminal() {
			return samplingTerminalEvaluation(targetResult), true, nil
		}
		if targetResult.Success {
			observations.ConsistencyTarget = append(observations.ConsistencyTarget, normalizeDistributionText(targetResult.Output))
		} else {
			observations.ConsistencyTargetFailures++
		}
		if comparisonRun != nil {
			comparisonResult, comparisonErr := runSamplingRequest(ctx, *comparison, comparisonRun, anchor.Question, samplingConsistencyBudget, 0, comparisonMode, comparisonStream)
			if comparisonErr != nil {
				return Evaluation{}, false, comparisonErr
			}
			if comparisonTerminal() {
				return samplingTerminalEvaluation(comparisonResult), true, nil
			}
			if comparisonResult.Success {
				observations.ConsistencyComparison = append(observations.ConsistencyComparison, normalizeDistributionText(comparisonResult.Output))
			} else {
				observations.ConsistencyComparisonFailures++
			}
		}
	}

	// paired_divergence：0 请求，读取本轮逐题对错格局。
	observations.Pairs = input.probeCapture.samplingPairs()
	return EvaluateSamplingStatistics(observations), false, nil
}

func runSamplingRequest(ctx context.Context, input Suite, run func(context.Context, Request) (Result, error), prompt string, maxOutputTokens int, temperature float64, mode string, stream bool) (Result, error) {
	request, err := input.tunedBasic(input.Model, prompt, mode, stream, maxOutputTokens, temperature)
	if err != nil {
		return Result{}, err
	}
	return run(ctx, request)
}

func samplingTerminalEvaluation(result Result) Evaluation {
	evidence := map[string]any{
		"requestFailure": true, "excludedFromScoring": true, "evidenceInsufficient": true,
		"terminalFailure": true, "httpStatus": result.HTTPStatus, "success": result.Success,
	}
	if result.ErrorMessage != "" {
		evidence["error"] = result.ErrorMessage
	}
	return withRetryEvidence(Evaluation{Kind: "sampling_statistics", Status: "skipped", Evidence: evidence}, result)
}

// EvaluateSamplingStatistics 汇总三个子机制的判定，输出单一家族证据项。
// 证据 JSON 结构（只含桶计数与统计量，不含任何响应正文）：
//
//	{
//	  "comparisonAttached": bool,        // 是否有可信对照
//	  "reasonCodes": []string,           // mixing_detected / systematic_divergence
//	                                    //（token_distribution 判分已降级，恒不产生码）
//	  "requestFailureCount": int,        // 非终局失败采样数
//	  "scoringProbeCount": int,          // 成功采样数（目标+对照）
//	  "partial": bool,                   // 任一采样失败（证据不完整，不形成族回执）
//	  "tokenDistribution": {
//	      "units": [{"unit": "coin", "sampleCount": 6,
//	                 "targetBuckets": {"正面": 4, "OTHER": 2},
//	                 "comparisonBuckets": {"正面": 3, "反面": 3},
//	                 "jsd": 0.352,                  // 双侧均有成功样本才有
//	                 "insufficient": true}],        // 单侧空桶单元不计均值
//	      "averageJsd": 0.21,             // 统计量（仅证据）：双侧均有样本单元的 JSD 均值；
//	                                    // 全单元不足则无该键
//	      "verdict": "evidence_only"      // 2026-10-02 复审判分降级：不判分
//	  },
//	  "consistencySampling": {
//	      "sampleCount": 6, "clusterCount": 1,
//	      "clusterSizes": {"1340": 6},     // 键为数值归一后的答案（≤50 字）
//	      "comparisonClusterCount": 2,     // 有对照时输出
//	      "comparisonAnomaly": bool,       // 对照分裂而目标一致
//	      "temperatureControlled": false,  // 仅温度不可控路径输出（CONCERN-A
//	                                    // 降级证据，便于排障）
//	      "verdict": "passed" | "failed" | "evidence_only" | "insufficient"
//	  },
//	  "pairedDivergence": {
//	      "pairCount": 9,
//	      "targetWrongComparisonCorrect": 6,  // McNemar b
//	      "targetCorrectComparisonWrong": 1,  // McNemar c
//	      "pValue": 0.0625, "systematic": bool,
//	      "verdict": "passed" | "failed" | "evidence_only"
//	  }
//	}
func EvaluateSamplingStatistics(observations SamplingObservations) Evaluation {
	reasonCodes := make([]string, 0, 2)
	requestFailures, successes := 0, 0
	partial := false

	// token_distribution 子机制（evidence_only）。2026-10-02 复审判分降级：
	// 6 采样经验桶估计器下零假设 E[avgJSD]≈0.39 已越过原 0.35 阈值、n=6
	// 不可判分；保留桶证据与逐单元 JSD 统计量，不产生任何 reasonCode。
	unitRecords := make([]map[string]any, 0, len(observations.TokenUnits))
	jsdValues := make([]float64, 0, len(observations.TokenUnits))
	for _, unit := range observations.TokenUnits {
		record := map[string]any{
			"unit":          unit.Key,
			"sampleCount":   bucketTotal(unit.TargetBuckets) + unit.TargetFailures,
			"targetBuckets": boundedBucketLabels(unit.TargetBuckets),
		}
		requestFailures += unit.TargetFailures + unit.ComparisonFailures
		successes += bucketTotal(unit.TargetBuckets) + bucketTotal(unit.ComparisonBuckets)
		if unit.TargetFailures > 0 || unit.ComparisonFailures > 0 {
			partial = true
		}
		if observations.ComparisonAttached {
			record["comparisonBuckets"] = boundedBucketLabels(unit.ComparisonBuckets)
			// 单侧空桶（任一侧 0 个成功样本）的单元 JSD 不参与均值，证据里记
			// insufficient；均值只聚合双侧均有样本的单元。
			if bucketTotal(unit.TargetBuckets) == 0 || bucketTotal(unit.ComparisonBuckets) == 0 {
				record["insufficient"] = true
			} else {
				jsd := JensenShannonDivergence(unit.TargetBuckets, unit.ComparisonBuckets)
				jsdValues = append(jsdValues, jsd)
				record["jsd"] = roundDistributionMetric(jsd)
			}
		}
		unitRecords = append(unitRecords, record)
	}
	averageJSD := 0.0
	if len(jsdValues) > 0 {
		for _, value := range jsdValues {
			averageJSD += value
		}
		averageJSD /= float64(len(jsdValues))
	}
	tokenDistributionRecord := map[string]any{"units": unitRecords, "verdict": "evidence_only"}
	if len(jsdValues) > 0 {
		tokenDistributionRecord["averageJsd"] = roundDistributionMetric(averageJSD)
	}

	// consistency_sampling 子机制。
	targetClusters := clusterCounts(observations.ConsistencyTarget)
	consistencyRecord := map[string]any{
		"sampleCount":  len(observations.ConsistencyTarget),
		"clusterCount": len(targetClusters),
		"clusterSizes": boundedBucketLabels(targetClusters),
	}
	requestFailures += observations.ConsistencyTargetFailures + observations.ConsistencyComparisonFailures
	successes += len(observations.ConsistencyTarget) + len(observations.ConsistencyComparison)
	if observations.ConsistencyTargetFailures > 0 || observations.ConsistencyComparisonFailures > 0 {
		partial = true
	}
	consistencyVerdict := "insufficient"
	if len(observations.ConsistencyTarget) > 0 {
		if observations.TemperatureUncontrolled {
			// CONCERN-A：温度不可控路径（Codex 适配 / Anthropic 协议）上
			// temperature=0 同题一致前提不成立——采样运行在默认温度，诚实
			// 账户（尤其弱模型）答案天然分裂。簇结构证据照常落库，verdict 恒
			// evidence_only，不产生 mixing_detected；对照分裂同理可能是温度
			// 噪声，不再记 comparisonAnomaly。token_distribution 不受影响
			// （本就 evidence_only）；paired_divergence 不受影响（配对检验对
			// 温度不敏感：两侧同条件采样，McNemar 的 p 值处理噪声）。
			consistencyVerdict = "evidence_only"
			consistencyRecord["temperatureControlled"] = false
		} else if len(targetClusters) >= 2 {
			consistencyVerdict = "failed"
			reasonCodes = append(reasonCodes, "mixing_detected")
		} else {
			consistencyVerdict = "passed"
			if len(observations.ConsistencyComparison) > 0 && len(clusterCounts(observations.ConsistencyComparison)) >= 2 {
				// 对照分裂而目标一致：目标按 passed，记录对照异常。
				consistencyRecord["comparisonAnomaly"] = true
			}
		}
	}
	if len(observations.ConsistencyComparison) > 0 {
		consistencyRecord["comparisonClusterCount"] = len(clusterCounts(observations.ConsistencyComparison))
	}
	consistencyRecord["verdict"] = consistencyVerdict

	// paired_divergence 子机制（McNemar 精确二项）。
	pairedRecord := map[string]any{"pairCount": len(observations.Pairs)}
	pairedVerdict := "evidence_only"
	if observations.PairedAttached && len(observations.Pairs) > 0 {
		b, c := 0, 0
		for _, pair := range observations.Pairs {
			if !pair.TargetCorrect && pair.ComparisonCorrect {
				b++
			}
			if pair.TargetCorrect && !pair.ComparisonCorrect {
				c++
			}
		}
		pValue := ExactBinomialTwoSided(b, b+c)
		systematic := pValue < samplingBinomialAlpha && b > c
		pairedRecord["targetWrongComparisonCorrect"] = b
		pairedRecord["targetCorrectComparisonWrong"] = c
		pairedRecord["pValue"] = roundDistributionMetric(pValue)
		pairedRecord["systematic"] = systematic
		if systematic {
			pairedVerdict = "failed"
			reasonCodes = append(reasonCodes, "systematic_divergence")
		} else {
			pairedVerdict = "passed"
		}
	}
	pairedRecord["verdict"] = pairedVerdict

	status := "passed"
	if containsString(reasonCodes, "mixing_detected") || containsString(reasonCodes, "systematic_divergence") {
		status = "failed"
	}
	if successes == 0 {
		// 全部采样请求失败（非终局）：证据不足，不伪造成失败。
		return Evaluation{Kind: "sampling_statistics", Status: "skipped", Evidence: map[string]any{
			"requestFailure": true, "excludedFromScoring": true, "evidenceInsufficient": true,
			"requestFailureCount": requestFailures, "scoringProbeCount": 0,
		}}
	}
	evidence := map[string]any{
		"comparisonAttached":  observations.ComparisonAttached,
		"reasonCodes":         reasonCodes,
		"requestFailureCount": requestFailures,
		"scoringProbeCount":   successes,
		"tokenDistribution":   tokenDistributionRecord,
		"consistencySampling": consistencyRecord,
		"pairedDivergence":    pairedRecord,
	}
	if partial {
		evidence["partial"] = true
	}
	return Evaluation{Kind: "sampling_statistics", Status: status, Score: 0, MaxScore: 0, Evidence: evidence}
}

// JensenShannonDivergence 按答案桶计数计算 base-2 Jensen-Shannon 散度
// （同分布 → 0，完全不相交 → 1）。零计数桶按 0log0=0 处理，不做平滑。
func JensenShannonDivergence(left, right map[string]int) float64 {
	leftTotal, rightTotal := bucketTotal(left), bucketTotal(right)
	if leftTotal == 0 && rightTotal == 0 {
		return 0
	}
	if leftTotal == 0 || rightTotal == 0 {
		return 1
	}
	labels := make(map[string]struct{}, len(left)+len(right))
	for label := range left {
		labels[label] = struct{}{}
	}
	for label := range right {
		labels[label] = struct{}{}
	}
	divergence := 0.0
	for label := range labels {
		p := float64(left[label]) / float64(leftTotal)
		q := float64(right[label]) / float64(rightTotal)
		m := (p + q) / 2
		if p > 0 {
			divergence += 0.5 * p * math.Log2(p/m)
		}
		if q > 0 {
			divergence += 0.5 * q * math.Log2(q/m)
		}
	}
	return divergence
}

// ExactBinomialTwoSided 是精确双侧二项检验（McNemar 口径）：n 个不一致对中
// 目标错 b 个，检验 b/(b+c) 是否显著偏离 0.5。p = 2·min(P(X≤b), P(X≥b))
// （X~Bin(n,0.5)），截断到 1。小样本不做正态近似。
func ExactBinomialTwoSided(successes, trials int) float64 {
	if trials <= 0 || successes < 0 || successes > trials {
		return 1
	}
	// 迭代计算 C(n,i)/2^n 的累计：cdf(k) = Σ_{i≤k} C(n,i)/2^n。
	power := math.Ldexp(1, trials) // 2^n
	coefficient := 1.0             // C(n,0)
	cdf := 0.0
	for i := 0; i <= successes-1; i++ {
		cdf += coefficient / power
		coefficient = coefficient * float64(trials-i) / float64(i+1)
	}
	lower := cdf + coefficient/power // P(X ≤ successes)
	upper := 1 - cdf                 // P(X ≥ successes)
	twoSided := 2 * math.Min(lower, upper)
	if twoSided > 1 || math.IsNaN(twoSided) {
		return 1
	}
	return twoSided
}

// normalizeSamplingAnswer 把采样输出归一化到单元桶标签；未见答案入 OTHER。
func normalizeSamplingAnswer(unit samplingUnitDefinition, output string) string {
	text := strings.ToLower(strings.TrimSpace(output))
	text = strings.Trim(text, "`\"'“”‘’。，,.!！?？ \t\n\r")
	if unit.key == "coin" {
		switch {
		case strings.Contains(text, "正") || strings.Contains(text, "head"):
			return "正面"
		case strings.Contains(text, "反") || strings.Contains(text, "tail"):
			return "反面"
		default:
			return samplingOtherBucket
		}
	}
	if unit.numeric {
		match := samplingIntegerPattern.FindString(text)
		if match == "" {
			return samplingOtherBucket
		}
		number, err := strconv.Atoi(strings.NewReplacer(",", "", "，", "").Replace(match))
		if err != nil || number < 1 || number > 100 {
			return samplingOtherBucket
		}
		return truncateIdentityText(strconv.Itoa(number), samplingBucketLabelLimit)
	}
	if unit.key == "color" {
		if mapped, ok := samplingEnglishColorPattern[text]; ok {
			return mapped
		}
		text = strings.TrimSuffix(text, "色")
	}
	if unit.key == "city" {
		text = strings.TrimSuffix(text, "市")
	}
	for _, label := range unit.vocab {
		if text == strings.ToLower(label) {
			return truncateIdentityText(label, samplingBucketLabelLimit)
		}
	}
	return samplingOtherBucket
}

// requestUserPrompt 从探针请求体提取单轮用户提示（行为题捕获用）；解析失败
// 返回空串，对应探针不参与配对检验。
func requestUserPrompt(request Request) string {
	payload := parseJSONObject(string(request.Body))
	if payload == nil {
		return ""
	}
	protocol := request.Protocol
	if protocol == "" {
		protocol = modelcheckprofile.ProtocolOpenAIResponses
	}
	switch protocol {
	case modelcheckprofile.ProtocolOpenAIResponses:
		if text, ok := payload["input"].(string); ok {
			return text
		}
		for _, entry := range list(payload["input"]) {
			record := asRecord(entry)
			for _, content := range list(record["content"]) {
				if text := asText(asRecord(content)["text"]); text != "" {
					return text
				}
			}
		}
		return ""
	case modelcheckprofile.ProtocolOpenAIChat, modelcheckprofile.ProtocolAnthropic:
		for index := len(list(payload["messages"])) - 1; index >= 0; index-- {
			record := asRecord(list(payload["messages"])[index])
			if record["role"] != "user" {
				continue
			}
			if text, ok := record["content"].(string); ok {
				return text
			}
		}
		return ""
	case modelcheckprofile.ProtocolGeminiNative:
		for _, entry := range list(payload["contents"]) {
			record := asRecord(entry)
			if record["role"] != "" && record["role"] != "user" {
				continue
			}
			for _, part := range list(record["parts"]) {
				if text := asText(asRecord(part)["text"]); text != "" {
					return text
				}
			}
		}
		return ""
	default:
		return ""
	}
}

// behaviorProbeKeyForPrompt 将行为题提示映射回行为题 key（exact 匹配）。
func behaviorProbeKeyForPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return ""
	}
	for _, probe := range behaviorProbes {
		if probe.Prompt == trimmed {
			return probe.Key
		}
	}
	return ""
}

func bucketTotal(buckets map[string]int) int {
	total := 0
	for _, count := range buckets {
		total += count
	}
	return total
}

// boundedBucketLabels 复制桶计数并截断桶标签到 ≤50 字（§17 统计口径）。
func boundedBucketLabels(buckets map[string]int) map[string]int {
	result := make(map[string]int, len(buckets))
	for label, count := range buckets {
		result[truncateIdentityText(label, samplingBucketLabelLimit)] = count
	}
	return result
}

// clusterCounts 聚类一致性采样答案。含整数的答案先做数值归一（提取首个整数
// 序列为规范形），「答案1340」「1340」「1,340」归同簇，防止锚点数值题的
// 措辞差异被判互斥簇误报 mixing_detected。
func clusterCounts(answers []string) map[string]int {
	clusters := map[string]int{}
	for _, answer := range answers {
		clusters[truncateIdentityText(normalizeConsistencyClusterKey(answer), samplingBucketLabelLimit)]++
	}
	return clusters
}

// normalizeConsistencyClusterKey 把一致性答案归一为聚类键：能提取出整数的
// 答案以首个整数序列（千分位逗号展开）为规范形，其余按原文。
func normalizeConsistencyClusterKey(answer string) string {
	if match := identityIntegerPattern.FindString(answer); match != "" {
		if cleaned := strings.NewReplacer(",", "", "，", "").Replace(match); cleaned != "" {
			return cleaned
		}
	}
	return answer
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// SamplingComparisonUnavailableSkip 输出对照模型不可用时的构造性跳过项
// （不发起采样请求，证据口径与 appendComparisonUnavailable 的
// distribution_similarity 跳过一致）。
func SamplingComparisonUnavailableSkip(model string) Evaluation {
	evidence := map[string]any{
		"requestFailure": true, "excludedFromScoring": true, "evidenceInsufficient": true,
		"modelUnavailable": true, "reason": "comparison_model_unavailable",
	}
	if strings.TrimSpace(model) != "" {
		evidence["model"] = model
	}
	return Evaluation{Kind: "sampling_statistics", Status: "skipped", Evidence: evidence}
}
