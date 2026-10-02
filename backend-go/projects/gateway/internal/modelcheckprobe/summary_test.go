package modelcheckprobe

import "testing"

func TestSummarizeChecksCapsFullWithoutTrustedComparisonAtLikely(t *testing.T) {
	// 自配 cross_model 退役后：即使全诊断族通过，无可信对比的 full run
	// 最高只能到 likely；high_confidence 需要独立账户的对照证据。
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, false, "full"); got.Level != "likely" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksHighConfidenceRequiresDiagnosticFamiliesFormed(t *testing.T) {
	// 缺任一诊断族（长上下文）时，可信对比也不得给出最高置信。
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "passed", Score: 10, MaxScore: 10},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level == "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksTrustedComparisonCanSatisfyHighConfidenceWithoutSelfCrossModel(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "passed", Score: 10, MaxScore: 10},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksMarksRequestFailureUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "warning", Score: 20, MaxScore: 35, Evidence: map[string]any{"requestFailure": true}},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
	}
	if got := SummarizeChecks(checks, false, "full"); got.Level != "uncertain" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksMarksFailedBasicUnavailableEvenWhenOtherCoresAnswer(t *testing.T) {
	// The Node oracle reports unavailable whenever the basic probe has no
	// successful response, regardless of other core results, so the run can
	// never authorize enforcement from a partial core family.
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "skipped", Evidence: map[string]any{"success": false, "requestFailure": true}},
		{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
		{Kind: "tool_calling", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
	}
	if got := SummarizeChecks(checks, false, "quick"); got.Level != "unavailable" {
		t.Fatalf("summary=%+v", got)
	}
}

// terminalCoreEvidence 复刻 requestFailureEvaluation 在重试边界耗尽后的
// 终态请求失败证据形状（evaluation.go：requestFailure+terminalFailure，
// skipped 且不进分母）。
func terminalCoreEvidence() map[string]any {
	return map[string]any{
		"success":              false,
		"requestFailure":       true,
		"terminalFailure":      true,
		"excludedFromScoring":  true,
		"evidenceInsufficient": true,
	}
}

// BUG-0259 / §1.2：任一核心协议探针第三次仍非 HTTP 200 时整轮判不可用，
// 不得按已执行项正常计分。四个核心探针逐一锁定（basic 的既有分支另有
// TestSummarizeChecksMarksFailedBasicUnavailableEvenWhenOtherCoresAnswer 锁定）。
func TestSummarizeChecksMarksCoreProbeTerminalFailureUnavailable(t *testing.T) {
	cases := []struct {
		name  string
		items []Evaluation
	}{
		{"tool 终态失败", []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
			{Kind: "tool_calling", Status: "skipped", Evidence: terminalCoreEvidence()},
			{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		}},
		{"structured 终态失败", []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "structured_output", Status: "skipped", Evidence: terminalCoreEvidence()},
			{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		}},
		{"stream 终态失败", []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "protocol_stream", Status: "skipped", Evidence: terminalCoreEvidence()},
			{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		}},
		{"Responses 协议 stream 终态失败", []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "responses_stream", Status: "skipped", Evidence: terminalCoreEvidence()},
			{Kind: "usage_shape", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := SummarizeChecks(test.items, false, "quick")
			if got.Level != "unavailable" || got.Message != "目标模型链路不可检测或上游不可用" {
				t.Fatalf("summary=%+v", got)
			}
		})
	}
}

// 终态判据必须同时要求 requestFailure 与 terminalFailure：仅 requestFailure
// （未到重试边界的请求失败）不触发整轮不可用，保持既有 uncertain/正常阶梯。
func TestSummarizeChecksRequiresTerminalFlagForUnavailable(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "tool_calling", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true, "evidenceInsufficient": true}},
	}
	if got := SummarizeChecks(checks, false, "quick"); got.Level == "unavailable" {
		t.Fatalf("非终态请求失败不得判整轮不可用: %+v", got)
	}
}

// §5.7 例外：题库环节请求失败只输出排除计分的 skipped 项（无 terminalFailure），
// 不触发整轮不可用。
func TestSummarizeChecksQuizRequestFailureStaysScoredRun(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "passed", Score: 10, MaxScore: 10},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "custom_quiz", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true, "evidenceInsufficient": true}},
	}
	got := SummarizeChecks(checks, true, "full")
	if got.Level == "unavailable" {
		t.Fatalf("题库请求失败按 §5.7 排除计分，不得判整轮不可用: %+v", got)
	}
	if got.Score != 100 || got.Level != "high_confidence" {
		t.Fatalf("排除计分的题库 skipped 项不得影响分数与层级: %+v", got)
	}
}

// 可信对比账户自身核心探针终态失败走 comparison 聚合的 uncertain 路径，
// 不把目标账户判成不可用。
func TestSummarizeChecksTrustedComparisonTerminalCoreStaysUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "trusted_comparison.tool_calling", Status: "skipped", Evidence: terminalCoreEvidence()},
		{Kind: "comparison", Status: "skipped", Evidence: map[string]any{"requestFailure": true, "excludedFromScoring": true}},
	}
	got := SummarizeChecks(checks, true, "full")
	if got.Level != "uncertain" {
		t.Fatalf("可信对比终态失败应保持 uncertain，不得判目标不可用: %+v", got)
	}
}

func TestSummarizeChecksMarksTrustedComparisonFailureUncertain(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "skipped", Evidence: map[string]any{"requestFailure": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "uncertain" {
		t.Fatalf("summary=%+v", got)
	}
}

// §16：identity_extraction 命中篡改（context_tampering）→ 整轮 suspicious，
// quick 与 full 都短路。
func TestSummarizeChecksShortCircuitsOnContextTampering(t *testing.T) {
	base := func(profile string) []Evaluation {
		return []Evaluation{
			{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
			{Kind: "structured_output", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
			{Kind: "tool_calling", Status: "passed", Score: 15, MaxScore: 15, Evidence: map[string]any{"success": true}},
		}
	}
	for _, profile := range []string{"quick", "full"} {
		checks := base(profile)
		checks = append(checks,
			Evaluation{Kind: "identity_selfreport", Status: "passed", Evidence: map[string]any{"consistent": true}},
			Evaluation{Kind: "identity_extraction", Status: "failed", Evidence: map[string]any{"reasonCode": "context_tampering", "contextTampering": true, "tamperedClaim": "gpt-6-luna"}},
		)
		got := SummarizeChecks(checks, false, profile)
		if got.Level != "suspicious" {
			t.Fatalf("profile=%s 篡改命中必须整轮 suspicious: %+v", profile, got)
		}
	}
	// 干净复读与注入警告不短路。
	checks := base("quick")
	checks = append(checks, Evaluation{Kind: "identity_extraction", Status: "warning", Evidence: map[string]any{"reasonCode": "injected_instructions_detected"}})
	if got := SummarizeChecks(checks, false, "quick"); got.Level == "suspicious" {
		t.Fatalf("注入警告不触发整轮短路: %+v", got)
	}
	// 可信对照账户自身的命中不进入目标短路。
	checks = base("full")
	checks = append(checks, Evaluation{Kind: "trusted_comparison.identity_extraction", Status: "failed", Evidence: map[string]any{"reasonCode": "context_tampering", "contextTampering": true}})
	if got := SummarizeChecks(checks, true, "full"); got.Level == "suspicious" {
		t.Fatalf("对照账户篡改走对照路径，不短路目标: %+v", got)
	}
}

// §17：mixing_detected / systematic_divergence 任一 → 整轮 suspicious。
// token_distribution 的 JSD 判分已于 2026-10-02 降级为 evidence_only：
// distribution_mismatch / distribution_divergent 不再产生，也不得短路。
func TestSummarizeChecksShortCircuitsOnSamplingStatistics(t *testing.T) {
	base := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
	}
	for _, code := range []string{"mixing_detected", "systematic_divergence"} {
		checks := append(append([]Evaluation(nil), base...), Evaluation{Kind: "sampling_statistics", Status: "failed", Evidence: map[string]any{"reasonCodes": []string{code}}})
		got := SummarizeChecks(checks, false, "full")
		if got.Level != "suspicious" {
			t.Fatalf("%s 必须整轮 suspicious: %+v", code, got)
		}
	}
	// 已降级的 distribution 两码（仅历史证据可能残留）不短路，走正常阶梯。
	for _, code := range []string{"distribution_mismatch", "distribution_divergent"} {
		checks := append(append([]Evaluation(nil), base...), Evaluation{Kind: "sampling_statistics", Status: "failed", Evidence: map[string]any{"reasonCodes": []string{code}}})
		if got := SummarizeChecks(checks, false, "full"); got.Level == "suspicious" {
			t.Fatalf("已降级码 %s 不得短路: %+v", code, got)
		}
	}
	// 证据性通过（无短路码）不短路。
	checks := append(append([]Evaluation(nil), base...), Evaluation{Kind: "sampling_statistics", Status: "passed", Evidence: map[string]any{"reasonCodes": []string{}}})
	if got := SummarizeChecks(checks, false, "full"); got.Level == "suspicious" {
		t.Fatalf("证据性通过不短路: %+v", got)
	}
}

func TestSummarizeChecksLetsTrustedComparisonWarningUseScoreLadder(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "warning", Score: 8, MaxScore: 10, Evidence: map[string]any{"evidenceInsufficient": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level != "likely" {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeChecksRequiresTrustedComparisonAggregateForHighConfidence(t *testing.T) {
	checks := []Evaluation{
		{Kind: "protocol_basic", Status: "passed", Score: 10, MaxScore: 10, Evidence: map[string]any{"success": true}},
		{Kind: "behavior_probe", Status: "passed", Score: 35, MaxScore: 35},
		{Kind: "stability", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "long_context", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "distribution_similarity", Status: "passed", Score: 15, MaxScore: 15},
		{Kind: "comparison", Status: "skipped", Evidence: map[string]any{"evidenceInsufficient": true}},
	}
	if got := SummarizeChecks(checks, true, "full"); got.Level == "high_confidence" {
		t.Fatalf("summary=%+v", got)
	}
}
