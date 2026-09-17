// 全链路验收 Stage 2：mock 故障注入矩阵（计划 §3 R1-R5 / F1-F7 / G1-G2，
// 全部无真号、无外呼）。所有场景共享一个隔离夹具（真实 gateway + 真实 jobs
// worker + 场景控制器 mock 上游），资源按场景唯一命名互不干扰；wall budget
// 由即时 mock 场景与短故障循环保证（单场景最慢 ~15s，为 F7 的同账户重试间隔
// 与 R5 的 10s 首字截止所必需）。
//
// 断言证据源：
//   - mock 场景控制器的上游命中记录（key/model/scenario/时序）——一手观测；
//   - audit-logs 请求级审计（attempt 行 + gateway_metadata 事件）；
//   - 账户快照 API（status/cooldownUntil 状态机）；
//   - usage-records（gateway spool → jobs drain 交接链，见 fullchain_e2e_test.go
//     的回归 canary 头注；链路回归时逐场景 skip）。
package acceptance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// fullchainAccountSpec 描述一个 mock 账户（凭据 key + 场景脚本 + 附加字段）。
type fullchainAccountSpec struct {
	key             string
	script          []platformmock.Scenario
	defaultScenario platformmock.Scenario
	extra           map[string]any
}

// fullchainGroupSpec 描述一个分组及其账户。
type fullchainGroupSpec struct {
	groupType        string
	schedulingPolicy map[string]any
	accounts         []fullchainAccountSpec
}

// fullchainRoute 是一个场景的完整路由拓扑。
type fullchainRoute struct {
	groupIDs     []string
	accountIDs   []string
	upstreamKeys []string
	apiKey       string
	apiKeyID     string
	strategyID   string
}

// newRoute 组装：分组（含账户）→ 策略（默认按分组顺序 priority=i+1、weight=100
// 绑定，可用 bindings 覆盖）→ API Key。返回完整拓扑。
func (f *fullchainFixture) newRoute(tag, mode string, groupSpecs []fullchainGroupSpec, bindings []map[string]any, strategyExtra map[string]any) fullchainRoute {
	f.t.Helper()
	route := fullchainRoute{}
	for groupIndex, spec := range groupSpecs {
		groupID := f.createGroupWithType(fmt.Sprintf("全链路-%s-组%d", tag, groupIndex+1), orGroupType(spec.groupType), spec.schedulingPolicy)
		route.groupIDs = append(route.groupIDs, groupID)
		for accountIndex, account := range spec.accounts {
			key := account.key
			if key == "" {
				key = fullchainUpstreamKey(f.t, fmt.Sprintf("%s-g%d-a%d", tag, groupIndex+1, accountIndex+1))
			}
			accountID := f.createAccount(fmt.Sprintf("全链路-%s-组%d-账户%d", tag, groupIndex+1, accountIndex+1), key, groupID, account.extra)
			if len(account.script) > 0 {
				f.mock.script(key, account.script...)
			}
			if account.defaultScenario != "" {
				f.mock.setDefault(key, account.defaultScenario)
			}
			route.accountIDs = append(route.accountIDs, accountID)
			route.upstreamKeys = append(route.upstreamKeys, key)
		}
	}
	if bindings == nil {
		bindings = []map[string]any{}
		for index, groupID := range route.groupIDs {
			bindings = append(bindings, map[string]any{"groupId": groupID, "priority": index + 1, "weight": 100})
		}
	}
	route.strategyID = f.createStrategy(fmt.Sprintf("全链路-%s-策略", tag), mode, bindings, strategyExtra)
	route.apiKey = f.createAPIKey(fmt.Sprintf("全链路-%s-Key", tag), route.strategyID)
	route.apiKeyID = f.apiKeyIDByName(fmt.Sprintf("全链路-%s-Key", tag))
	return route
}

func orGroupType(value string) string {
	if value == "" {
		return "personal"
	}
	return value
}

// upstreamKeySequence 返回业务请求的上游命中时序（key 序列）。
func (f *fullchainFixture) upstreamKeySequence() []string {
	f.t.Helper()
	calls := f.mock.protocolCalls()
	out := make([]string, 0, len(calls))
	for _, call := range calls {
		out = append(out, call.Key)
	}
	return out
}

// waitAuditLabels 轮询该 API Key 全部审计的 gateway_metadata label 并集，
// 直到包含 want 全部标签（审计落库异步）。
//
// E2E-FINDING #11 已修复（F11）：根因是类型桥 auditUsageDispatcher 的
// JSON 往返丢弃 payload body（Body/HasBody 为 json:"-"）；gatewayusage.
// AuditLogPayloadInput 现以 Buffer base64 形式序列化 Body，label 恢复
// 可从 bodyText 解析。超时即 Fatal，不再跳过。
func (f *fullchainFixture) waitAuditLabels(t *testing.T, apiKeyID string, want []string, what string) []string {
	t.Helper()
	missing := func(labels []string) []string {
		set := map[string]bool{}
		for _, label := range labels {
			set[label] = true
		}
		out := []string{}
		for _, label := range want {
			if !set[label] {
				out = append(out, label)
			}
		}
		return out
	}
	deadline := time.Now().Add(20 * time.Second)
	var union []string
	for time.Now().Before(deadline) {
		union = f.auditLabelUnion(apiKeyID)
		if len(missing(union)) == 0 {
			return union
		}
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatalf("audit labels not %s in time; want %v got %v\npayload triage: %s", what, want, union, f.auditPayloadTriage(apiKeyID))
	return nil
}

// auditPayloadTriage 在 label 断言超时时输出审计载荷清单（partType/保留状态），
// 用于区分「事件未发生」与「载荷被省略/解析失败」。
func (f *fullchainFixture) auditPayloadTriage(apiKeyID string) string {
	f.t.Helper()
	pieces := []string{}
	for _, item := range f.findAuditLogs(apiKeyID) {
		detail := f.auditLogDetail(str(item["id"]))
		summary := fmt.Sprintf("log=%s outcome=%s attempts=%d payloads=[", detail.ID, detail.Raw["auditOutcome"], len(detail.Attempts))
		payloads, _ := detail.Raw["payloads"].([]any)
		for _, raw := range payloads {
			part, _ := raw.(map[string]any)
			if part == nil {
				continue
			}
			summary += fmt.Sprintf("%s(hasBody=%v,capture=%s,drop=%s,size=%v)", part["partType"], part["hasBody"], part["captureStatus"], part["dropReason"], part["sizeBytes"])
		}
		summary += "]"
		pieces = append(pieces, summary)
	}
	return strings.Join(pieces, "; ")
}

// auditLabelUnion 拉取该 API Key 当前全部审计日志的 label 并集。
func (f *fullchainFixture) auditLabelUnion(apiKeyID string) []string {
	f.t.Helper()
	seen := map[string]bool{}
	out := []string{}
	for _, item := range f.findAuditLogs(apiKeyID) {
		detail := f.auditLogDetail(str(item["id"]))
		for _, label := range detail.GatewayLabels {
			if !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
		}
	}
	return out
}

// fullchainGatewayMetadataByLabel 从审计详情提取指定 label 的全部
// gateway_metadata 载荷 metadata map（bodyText JSON 形如
// {"type":"gateway_metadata","label":...,"metadata":{...}}，按落库顺序）；
// 取不到（审计异步窗口 / 载荷被省略）返回空切片，由调用方裁决语义。
func (f *fullchainFixture) fullchainGatewayMetadataByLabel(log fullchainAuditLog, label string) []map[string]any {
	f.t.Helper()
	out := []map[string]any{}
	payloads, _ := log.Raw["payloads"].([]any)
	for _, raw := range payloads {
		part, _ := raw.(map[string]any)
		if part == nil || str(part["partType"]) != "gateway_metadata" {
			continue
		}
		_, meta := f.admin.do(http.MethodGet,
			"/__aisys__/api/audit-logs/"+log.ID+"/payloads/"+str(part["id"]), nil, wantStatus(http.StatusOK))
		var decoded map[string]any
		if err := json.Unmarshal([]byte(str(data(meta)["bodyText"])), &decoded); err != nil {
			continue
		}
		if str(decoded["label"]) != label {
			continue
		}
		if metadata, ok := decoded["metadata"].(map[string]any); ok {
			out = append(out, metadata)
		}
	}
	return out
}

// attemptStatus 安全取 attempt 的上游状态码。
func attemptStatus(attempt map[string]any) *int64 {
	if attempt == nil {
		return nil
	}
	code, ok := attempt["upstreamStatusCode"].(float64)
	if !ok {
		return nil
	}
	value := int64(code)
	return &value
}

// chatBody 构造非流式 chat 请求体。
func chatBody(model, prompt string) string {
	return fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"%s"}]}`, model, prompt)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// ---------------------------------------------------------------------------
// R 路由模式
// ---------------------------------------------------------------------------

// R1 normal：普通路由固定单分组（网关契约：normal 只能绑定一个启用分组），
// 组内 2 个 mock-ok 账户按固定优先序，连续请求恒命中第一账户。
func fullchainR1(t *testing.T, f *fullchainFixture) {
	route := f.newRoute("R1", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{}, {}}},
	}, nil, nil)

	for i := 0; i < 3; i++ {
		response := f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("R1-%d", i)))
		if response.Status != http.StatusOK {
			t.Fatalf("R1 request %d status=%d body=%s", i, response.Status, response.Body)
		}
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[0])); got != 3 {
		t.Fatalf("R1 first-account hits=%d want 3", got)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 0 {
		t.Fatalf("R1 second-account hits=%d want 0", got)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			success := 0
			for _, record := range records {
				if record.Success && record.AccountID == route.accountIDs[0] {
					success++
				}
			}
			return success == 3
		}, "3 success rows all on the first account")
	})
}

// R2 failover：主组恒 500 → 备组接管 200；主组恢复后流量回主组。
func fullchainR2(t *testing.T, f *fullchainFixture) {
	primaryKey := fullchainUpstreamKey(t, "R2-primary")
	route := f.newRoute("R2", "failover", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: primaryKey, defaultScenario: platformmock.ScenarioStatus500}}},
		{accounts: []fullchainAccountSpec{{}}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "R2-故障转移"))
	if response.Status != http.StatusOK {
		t.Fatalf("R2 failover status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(primaryKey)); got < 1 {
		t.Fatalf("R2 primary hits=%d want >=1", got)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("R2 backup hits=%d want 1", got)
	}

	// 主组恢复：清掉恒 500 注入后，下一个请求应回到主组（failover 每请求
	// 先试主绑定）。
	f.mock.clearKey(primaryKey)
	recovered := f.chat(route.apiKey, chatBody(acceptanceModel, "R2-主组恢复"))
	if recovered.Status != http.StatusOK {
		t.Fatalf("R2 recovered status=%d body=%s", recovered.Status, recovered.Body)
	}
	if got := len(f.mock.protocolCallsByKey(primaryKey)); got != 1 {
		t.Fatalf("R2 primary recovery hits=%d want 1", got)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("R2 backup hits after recovery=%d want 1（不应再命中备组）", got)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			backupSuccess, primarySuccess := 0, 0
			for _, record := range records {
				if !record.Success {
					continue
				}
				if record.AccountID == route.accountIDs[1] {
					backupSuccess++
				}
				if record.AccountID == route.accountIDs[0] {
					primarySuccess++
				}
			}
			return backupSuccess == 1 && primarySuccess >= 1
		}, "backup success row + recovered primary success row")
	})
}

// R3 round_robin：3 组 mock-ok，连续 6 请求轮转序列精确匹配。
func fullchainR3(t *testing.T, f *fullchainFixture) {
	route := f.newRoute("R3", "round_robin", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{}}},
		{accounts: []fullchainAccountSpec{{}}},
		{accounts: []fullchainAccountSpec{{}}},
	}, nil, nil)

	for i := 0; i < 6; i++ {
		response := f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("R3-%d", i)))
		if response.Status != http.StatusOK {
			t.Fatalf("R3 request %d status=%d body=%s", i, response.Status, response.Body)
		}
	}
	sequence := f.upstreamKeySequence()
	want := []string{
		route.upstreamKeys[0], route.upstreamKeys[1], route.upstreamKeys[2],
		route.upstreamKeys[0], route.upstreamKeys[1], route.upstreamKeys[2],
	}
	if len(sequence) < 6 {
		t.Fatalf("R3 upstream calls=%d want >=6: %v", len(sequence), sequence)
	}
	got := sequence[len(sequence)-6:]
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("R3 rotation sequence mismatch at %d: got %v want %v", index, got, want)
		}
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			perGroup := map[string]int{}
			for _, record := range records {
				if record.Success {
					perGroup[record.GroupID]++
				}
			}
			return perGroup[route.groupIDs[0]] == 2 && perGroup[route.groupIDs[1]] == 2 && perGroup[route.groupIDs[2]] == 2
		}, "2 success rows per group")
	})
}

// R4 weighted 2:1：12 请求按 2:1 比例分布（±1 容差）。
func fullchainR4(t *testing.T, f *fullchainFixture) {
	route := f.newRoute("R4", "weighted", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{}}},
		{accounts: []fullchainAccountSpec{{}}},
	}, nil, nil)
	// 默认绑定创建后重绑为 2:1 权重（R4 的被测契约）。
	f.rebindWeights(t, route, map[string]int{route.groupIDs[0]: 2, route.groupIDs[1]: 1})

	for i := 0; i < 12; i++ {
		response := f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("R4-%d", i)))
		if response.Status != http.StatusOK {
			t.Fatalf("R4 request %d status=%d body=%s", i, response.Status, response.Body)
		}
	}
	heavy := len(f.mock.protocolCallsByKey(route.upstreamKeys[0]))
	light := len(f.mock.protocolCallsByKey(route.upstreamKeys[1]))
	if absDiff(heavy, 8) > 1 || absDiff(light, 4) > 1 || heavy+light != 12 {
		t.Fatalf("R4 weighted distribution got heavy=%d light=%d want 8:4 (±1)", heavy, light)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			counts := map[string]int{}
			for _, record := range records {
				if record.Success {
					counts[record.GroupID]++
				}
			}
			return absDiff(counts[route.groupIDs[0]], 8) <= 1 && absDiff(counts[route.groupIDs[1]], 4) <= 1
		}, "weighted distribution rows")
	})
}

// rebindWeights 以自定义权重重绑策略分组（R4 的 2:1）。
func (f *fullchainFixture) rebindWeights(t *testing.T, route fullchainRoute, weights map[string]int) {
	t.Helper()
	bindings := []any{}
	for index, groupID := range route.groupIDs {
		weight := weights[groupID]
		if weight == 0 {
			weight = 100
		}
		bindings = append(bindings, map[string]any{"groupId": groupID, "priority": index + 1, "weight": weight})
	}
	_, payload := f.admin.do(http.MethodGet, "/__aisys__/api/route-strategies/"+route.strategyID+"/edit-basic", nil, wantStatus(http.StatusOK))
	expectedUpdatedAt := str(data(payload)["updatedAt"])
	if expectedUpdatedAt == "" {
		t.Fatalf("route strategy edit-basic payload wrong: %#v", payload)
	}
	_, patched := f.admin.do(http.MethodPatch, "/__aisys__/api/route-strategies/"+route.strategyID,
		map[string]any{"expectedUpdatedAt": expectedUpdatedAt, "groupBindings": bindings}, wantStatus(http.StatusOK))
	if data(patched) == nil {
		t.Fatalf("route strategy weight rebind failed: %#v", patched)
	}
}

// R5 normal+speed_first：慢首 token 超 firstByteDeadlineMs → 切下一候选。
//
// 契约：切换候选的前提是账户进入延迟降级（slowTriggerCount 次慢观测），所以
// 第 1 个请求只完成一次慢观测（不切换），第 2 个请求观测达标降级后当场切换。
// 场景用控制器原生 slow_body_first_byte（头立即回、body 拖 12s），因为 Go 网关
// 的 firstByteDeadlineMs 只覆盖 body 首字节（见 E2E-FINDING #3）。
func fullchainR5(t *testing.T, f *fullchainFixture) {
	slowKey := fullchainUpstreamKey(t, "R5-slow")
	route := f.newRoute("R5", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: slowKey, defaultScenario: fullchainSlowBodyScenario},
			{},
		}},
	}, nil, map[string]any{
		"normalRoutingConfig": map[string]any{
			"schedulingPreference": "speed_first",
			"firstByteDeadlineMs":  10000,
			"speedFirstConfig":     map[string]any{"slowTriggerCount": 2},
		},
	})

	// 第 1 个请求：慢观测 #1（不切换，等慢账户 12s 后返回 200）。
	first := f.chat(route.apiKey, chatBody(acceptanceModel, "R5-慢观测"))
	if first.Status != http.StatusOK {
		t.Fatalf("R5 warmup status=%d body=%s", first.Status, first.Body)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 0 {
		t.Fatalf("R5 warmup should not cut over; fast hits=%d", got)
	}

	// 第 2 个请求起：慢观测达标 → 账户降级 → 快账户接管。
	// 语义裁决（E2E-FINDING #4，docs/functions/普通路由速度优先延迟切换设计.md）：
	// 文档第 9/70 行明确「首字观察阈值不能直接等价于当前请求必须切号」
	// 「慢样本未触发 latency_degraded 时当前上游继续读取」——慢请求当次
	// 读完、由重排/切换在下一次选号接管是文档内的合法路径。决策面已实现
	// Node 契约：degradedForCutover 含触发降级的那次观测
	//（chain_v1.go degradedForCutover := alreadyDegraded || slowResult.Degraded），
	// 预算/剩余候选就绪时当次即 Abort 切换（cutoverAt==2）；预算不可用或
	// attach 竞争失败时回落软截止继续读 + 下次请求重排接管（cutoverAt==3）。
	// 两者都满足计划 R5 的验收意图（慢首 token 超截止 → 快候选接管）。
	cutoverAt := 0
	elapsed := time.Duration(0)
	var second fullchainChatResponse
	for requestIndex := 2; requestIndex <= 4; requestIndex++ {
		startedAt := time.Now()
		second = f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("R5-速度优先-%d", requestIndex)))
		elapsed = time.Since(startedAt)
		if second.Status != http.StatusOK {
			t.Fatalf("R5 request %d status=%d body=%s", requestIndex, second.Status, second.Body)
		}
		if len(f.mock.protocolCallsByKey(route.upstreamKeys[1])) > 0 {
			cutoverAt = requestIndex
			break
		}
	}
	// 契约自检：策略配置已按 speed_first + 首字截止存储。
	_, strategyDetail := f.admin.do(http.MethodGet, "/__aisys__/api/route-strategies/"+route.strategyID, nil, wantStatus(http.StatusOK))
	config, _ := data(strategyDetail)["normalRoutingConfig"].(map[string]any)
	if config == nil || config["schedulingPreference"] != "speed_first" {
		t.Fatalf("R5 strategy normalRoutingConfig not speed_first: %#v", data(strategyDetail)["normalRoutingConfig"])
	}
	if cutoverAt == 0 {
		t.Fatalf("R5 fast account never took over within diagnostic window; slowHits=%d", len(f.mock.protocolCallsByKey(slowKey)))
	}
	if cutoverAt == 2 && elapsed >= 11*time.Second {
		t.Fatalf("R5 same-request cutover did not meet deadline: elapsed=%s", elapsed)
	}
	if cutoverAt > 2 {
		// E2E-FINDING #4：降级触发的观测未同请求切换（快候选在同请求未被
		// 尝试），切换延后到下一次请求的时延降级重排。证据：请求 2 elapsed
		// ~12s 且 fastHits=0、请求 3 立即命中快账户。blocked 原因元数据
		// （normal_route_speed_first_slow_observed.retryBlockedReason）随
		// E2E-FINDING #2 修复（捕获上限回落默认）已可读。
		// R5 FINDING#4 审计闭环：提取降级观测（degraded==true；warmup 请求
		// 的 slowCount 未达标观测合法报 slow_observation_not_degraded，不算
		// 缺陷信号）载荷的 retryBlockedReason 留档。取到且值 ==
		// slow_observation_not_degraded → Fatal（降级观测未生效的缺陷信号）；
		// 其他值或取不到（审计异步窗口）保持本日志继续，不改变放行语义。
		retryBlockedReason := ""
		var degradedMetadata map[string]any
		for _, item := range f.findAuditLogs(route.apiKeyID) {
			detail := f.auditLogDetail(str(item["id"]))
			for _, metadata := range f.fullchainGatewayMetadataByLabel(detail, "normal_route_speed_first_slow_observed") {
				if degradedFlag, _ := metadata["degraded"].(bool); !degradedFlag {
					continue
				}
				if reason, ok := metadata["retryBlockedReason"].(string); ok {
					retryBlockedReason = reason
				}
				degradedMetadata = metadata
				break
			}
			if degradedMetadata != nil {
				break
			}
		}
		t.Logf("E2E-FINDING #4: speed-first same-request cutover did not fire on the degrade-triggering observation; takeover happened via latency-degradation reorder on request %d; audit retryBlockedReason=%q degradedMetadata=%v", cutoverAt, retryBlockedReason, degradedMetadata)
		if retryBlockedReason == "slow_observation_not_degraded" {
			t.Fatalf("R5 FINDING #4 defect signal: degrade-triggering observation still reports retryBlockedReason=slow_observation_not_degraded; metadata=%v", degradedMetadata)
		}
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("R5 fast account hits=%d want 1", got)
	}
	if elapsed >= 11*time.Second {
		t.Fatalf("R5 takeover request still waited on the slow account: elapsed=%s", elapsed)
	}
	if got := len(f.mock.protocolCallsByKey(slowKey)); got != 2 {
		t.Fatalf("R5 slow account hits=%d want 2", got)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("R5 fast account hits=%d want 1", got)
	}

	// 审计证据：同请求切换时应有「慢+快」两条 attempt 的审计；重排接管时
	// 每条审计 1 个 attempt，接管请求的账户是快账户。
	if cutoverAt == 2 {
		detail := f.waitAuditLogDetail(t, route.apiKeyID, func(log fullchainAuditLog) bool {
			return len(log.Attempts) >= 2
		}, "with two attempts (slow + fast)")
		if str(detail.Attempts[0]["accountId"]) != route.accountIDs[0] || str(detail.Attempts[1]["accountId"]) != route.accountIDs[1] {
			t.Fatalf("R5 cutover attempts wrong: %#v", detail.Attempts)
		}
	} else {
		f.waitAuditLogDetail(t, route.apiKeyID, func(log fullchainAuditLog) bool {
			return len(log.Attempts) == 1 && str(log.Attempts[0]["accountId"]) == route.accountIDs[1]
		}, "with a fast-account attempt after reorder takeover")
	}

	// 慢账户状态中性（速度优先本地决策不写账户状态）。
	snapshot := f.accountSnapshot(route.accountIDs[0])
	if str(snapshot["status"]) != "active" || snapshot["cooldownUntil"] != nil {
		t.Fatalf("R5 slow account state not neutral: status=%v cooldownUntil=%v", snapshot["status"], snapshot["cooldownUntil"])
	}

	t.Run("slow_header_probe_finding", func(t *testing.T) {
		// 语义裁决（E2E-FINDING #3 修订，2026-09-18）：此探针此前固化「头阶段
		// 不切号」，但该断言建立在 NormalRouteFirstByteAttemptCoordinator 零值
		// 缺陷（state 恒空 → AttachReservation 恒 false → 决策恒 Continue）之上。
		// Node 契约：request.ts:261-266 头阶段同样挂 firstByteDeadlineMs timer +
		// onFirstByteDeadline 决策（upstream-attempts.ts:97-101 传入），决策闭包
		// （routes.ts:884-920）不区分头/体 transport——头阶段拖延到截止进入与
		// body 阶段同一条决策链。设计文档第 68/69 行的「首字按 body 字节计」是
		// 观测口径，不是头阶段豁免。
		//
		// 确定性断言（独立 scope 单请求，slowTriggerCount=2 首次观测未降级，
		// Continue 是合法决策）：① 请求等满上游 12s 出头完成（截止未中断流）；
		// ② 审计出现 normal_route_speed_first_slow_observed——证明头阶段截止
		// 进入速度优先决策链（零值缺陷下该审计不存在）。切号链路由 R5 主测
		// （同闭包、喂满降级）覆盖。
		headerSlowKey := fullchainUpstreamKey(t, "R5-headerslow")
		probe := f.newRoute("R5B", "normal", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				{key: headerSlowKey, defaultScenario: platformmock.ScenarioSlowFirstByte},
				{},
			}},
		}, nil, map[string]any{
			"normalRoutingConfig": map[string]any{
				"schedulingPreference": "speed_first",
				"firstByteDeadlineMs":  10000,
				"speedFirstConfig":     map[string]any{"slowTriggerCount": 2},
			},
		})
		startedAt := time.Now()
		response := f.chat(probe.apiKey, chatBody(acceptanceModel, "R5B-慢头探针"))
		elapsed := time.Since(startedAt)
		if response.Status != http.StatusOK {
			t.Fatalf("R5B probe status=%d body=%s", response.Status, response.Body)
		}
		// 修复后确定性时线：10s 头阶段截止 → 首次慢观测（未降级）Continue；
		// 12s 慢头返回 → body 竞速立即第二次决策（实测已慢 → degraded →
		// cutover）→ 慢账户排除、fast 接管。请求总时长 ≈ 12s（慢头决定），
		// fast 恰好 1 次（切号接管）、slow 恰好 1 次（切号后排除）。
		if elapsed < 11*time.Second {
			t.Fatalf("R5B probe expected full slow-header wait (elapsed>=11s) but got %s", elapsed)
		}
		if got := len(f.mock.protocolCallsByKey(probe.upstreamKeys[1])); got != 1 {
			t.Fatalf("R5B probe expected fast takeover after second slow observation; fast hits=%d", got)
		}
		if got := len(f.mock.protocolCallsByKey(probe.upstreamKeys[0])); got != 1 {
			t.Fatalf("R5B probe expected slow account hit exactly once (excluded after cutover); slow hits=%d", got)
		}
		deadline := time.Now().Add(20 * time.Second)
		var observed []map[string]any
		for time.Now().Before(deadline) {
			for _, log := range f.findAuditLogs(probe.apiKeyID) {
				observed = f.fullchainGatewayMetadataByLabel(f.auditLogDetail(str(log["id"])), "normal_route_speed_first_slow_observed")
				if len(observed) > 0 {
					return
				}
			}
			time.Sleep(400 * time.Millisecond)
		}
		t.Fatalf("R5B probe：头阶段截止未产生 normal_route_speed_first_slow_observed 审计（决策链不可达）")
	})
}

// ---------------------------------------------------------------------------
// F 切号与状态
// ---------------------------------------------------------------------------

// F1 同账户瞬态重试：500 一次后 ok，同账户重试成功，无切号。
// 审计证据取 outcome=success_after_retry + 两条同账户 attempt + 请求级
// gateway_metadata 事件 same_account_retry_dispatch（label 可读性依赖审计
// 捕获上限回落默认——E2E-FINDING #2 曾因上限全零把全部载荷判 overflow）。
func fullchainF1(t *testing.T, f *fullchainFixture) {
	retryKey := fullchainUpstreamKey(t, "F1")
	route := f.newRoute("F1", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: retryKey, script: []platformmock.Scenario{platformmock.ScenarioStatus500}}}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F1-瞬态重试"))
	if response.Status != http.StatusOK {
		t.Fatalf("F1 status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(retryKey)); got != 2 {
		t.Fatalf("F1 same-account attempts=%d want 2（一次失败 + 一次重试）", got)
	}
	detail := f.waitAuditLogDetail(t, route.apiKeyID, func(log fullchainAuditLog) bool {
		return str(log.Raw["auditOutcome"]) == "success_after_retry"
	}, "with success_after_retry outcome")
	if len(detail.Attempts) != 2 {
		t.Fatalf("F1 audit attempts=%d want 2: %#v", len(detail.Attempts), detail.Attempts)
	}
	for _, attempt := range detail.Attempts {
		if str(attempt["accountId"]) != route.accountIDs[0] {
			t.Fatalf("F1 retry left the account: %#v", detail.Attempts)
		}
	}
	if attemptStatus(detail.Attempts[0]) == nil || *attemptStatus(detail.Attempts[0]) != 500 {
		t.Fatalf("F1 first attempt status wrong: %#v", detail.Attempts[0])
	}
	if attemptStatus(detail.Attempts[1]) == nil || *attemptStatus(detail.Attempts[1]) != 200 {
		t.Fatalf("F1 second attempt status wrong: %#v", detail.Attempts[1])
	}

	// 请求级元数据：同账户重试派发事件以 gateway_metadata 载荷保留且 label
	// 可读（捕获上限默认回落生效，载荷不再被 active_capture_overflow 清空）。
	f.waitAuditLabels(t, route.apiKeyID, []string{"same_account_retry_dispatch"}, "same-account retry dispatch metadata event")

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			success, failed := 0, 0
			for _, record := range records {
				if record.AccountID != route.accountIDs[0] {
					continue
				}
				if record.Success {
					success++
				} else {
					failed++
				}
			}
			return success == 1 && failed == 1
		}, "failed + success rows on the same account")
	})
}

// F2 跨账户切换：A 恒 500 → B 接管；A 保持 active（opaque 错误不写状态）。
func fullchainF2(t *testing.T, f *fullchainFixture) {
	brokenKey := fullchainUpstreamKey(t, "F2-broken")
	route := f.newRoute("F2", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: brokenKey, defaultScenario: platformmock.ScenarioStatus500},
			{},
		}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F2-跨账户"))
	if response.Status != http.StatusOK {
		t.Fatalf("F2 status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(brokenKey)); got < 2 {
		t.Fatalf("F2 broken account attempts=%d want >=2（瞬态同账户重试后放弃）", got)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("F2 healthy account hits=%d want 1", got)
	}
	snapshot := f.accountSnapshot(route.accountIDs[0])
	if str(snapshot["status"]) != "active" {
		t.Fatalf("F2 broken account status=%v want active（opaque 非 2xx 不写状态）", snapshot["status"])
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			failureA, successB := 0, 0
			for _, record := range records {
				if record.AccountID == route.accountIDs[0] && !record.Success {
					failureA++
				}
				if record.AccountID == route.accountIDs[1] && record.Success {
					successB++
				}
			}
			return failureA >= 1 && successB == 1
		}, "failure row on A + success row on B")
	})
}

// F3 系统额度规则：403 + insufficient_quota → rate_limited + cooldown_until；
// clearFailureState 恢复后重新服务。
func fullchainF3(t *testing.T, f *fullchainFixture) {
	quotaKey := fullchainUpstreamKey(t, "F3")
	route := f.newRoute("F3", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: quotaKey, script: []platformmock.Scenario{platformmock.ScenarioStatus403}}}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F3-额度"))
	if response.Status != http.StatusServiceUnavailable {
		t.Fatalf("F3 exhausted status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(quotaKey)); got != 1 {
		t.Fatalf("F3 explicit policy attempts=%d want 1（显式冷却不做同账户重试）", got)
	}
	snapshot := f.accountSnapshot(route.accountIDs[0])
	if str(snapshot["status"]) != "rate_limited" {
		t.Fatalf("F3 account status=%v want rate_limited: %#v", snapshot["status"], snapshot)
	}
	// 详情读模型不投影 cooldownUntil（omitempty），从账户列表视图取。
	cooldownUntil := f.accountFieldFromList(route.accountIDs[0], "cooldownUntil")
	if cooldownUntil == "" {
		t.Fatalf("F3 cooldownUntil empty in list view: %#v", snapshot)
	}
	// 显式策略证据：403 opaque 不会写状态，唯一能写 rate_limited+cooldown 的
	// 路径是系统额度规则；lastErrorCode 作为补充证据记录（脱敏：仅错误码）。
	if code := str(snapshot["lastErrorCode"]); code != "" && !strings.Contains(strings.ToLower(code), "quota") {
		t.Logf("F3 lastErrorCode=%q（非 quota 族，记录备查）", code)
	}

	// 恢复（短 TTL 造数：clearFailureState，避免真实等待冷却 TTL）。管理面
	// 写入后网关运行时缓存经异步失效刷新，恢复请求允许短暂 503 重试窗。
	f.clearAccountFailure(route.accountIDs[0])
	recoveredStatus, recoveredBody := f.retryChatUntil(route.apiKey, chatBody(acceptanceModel, "F3-恢复"), 12*time.Second, http.StatusOK)
	if recoveredStatus != http.StatusOK {
		t.Fatalf("F3 recovered status=%d body=%s", recoveredStatus, recoveredBody)
	}
	if got := len(f.mock.protocolCallsByKey(quotaKey)); got != 2 {
		t.Fatalf("F3 attempts after recovery=%d want 2", got)
	}
}

// F4 显式错误策略：账户 error_handling_rules 命中 → error_disabled；PATCH 恢复。
func fullchainF4(t *testing.T, f *fullchainFixture) {
	disabledKey := fullchainUpstreamKey(t, "F4")
	route := f.newRoute("F4", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{
			key:    disabledKey,
			script: []platformmock.Scenario{platformmock.ScenarioStatus502},
			extra: map[string]any{
				"credentials": map[string]any{
					"error_handling_rules": []any{
						map[string]any{
							"enabled":      true,
							"name":         "验收502禁用规则",
							"priority":     1,
							"status_codes": []any{502},
							"action":       "error_disabled",
						},
					},
				},
			},
		}}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F4-禁用"))
	if response.Status != http.StatusServiceUnavailable {
		t.Fatalf("F4 exhausted status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(disabledKey)); got != 1 {
		t.Fatalf("F4 explicit policy attempts=%d want 1", got)
	}
	// 观察（报告单列，非缺陷）：error_disabled 动作落库为账户状态 "error"
	//（计划文本写作 error_disabled；当前账户状态词汇表无独立 error_disabled
	// 状态，禁用语义由 status=error + 调度屏蔽承载）。
	snapshot := f.accountSnapshot(route.accountIDs[0])
	if str(snapshot["status"]) != "error" {
		t.Fatalf("F4 account status=%v want error: %#v", snapshot["status"], snapshot)
	}

	f.clearAccountFailure(route.accountIDs[0])
	// 恢复断言：允许运行时缓存异步失效的短暂重试窗。
	recoveredStatus, recoveredBody := f.retryChatUntil(route.apiKey, chatBody(acceptanceModel, "F4-恢复"), 12*time.Second, http.StatusOK)
	if recoveredStatus != http.StatusOK {
		t.Fatalf("F4 recovered status=%d body=%s", recoveredStatus, recoveredBody)
	}
}

// retryChatUntil 重试同一请求直到目标状态码或超时（运行时缓存异步失效的
// 恢复窗口用）。
func (f *fullchainFixture) retryChatUntil(apiKey, body string, timeout time.Duration, want int) (int, string) {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	var last fullchainChatResponse
	for time.Now().Before(deadline) {
		last = f.chat(apiKey, body)
		if last.Status == want {
			return last.Status, last.Body
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last.Status, last.Body
}

// F5 流式已提交断流：mid_stream_close → 上游断流（upstream TCP reset，客户端
// 是观测方），无第二账户内容拼接。
func fullchainF5(t *testing.T, f *fullchainFixture) {
	streamKey := fullchainUpstreamKey(t, "F5-stream")
	route := f.newRoute("F5", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: streamKey, script: []platformmock.Scenario{platformmock.ScenarioMidStreamClose}},
			{},
		}},
	}, nil, nil)

	response := f.chat(route.apiKey, fmt.Sprintf(`{"model":"%s","stream":true,"messages":[{"role":"user","content":"F5-断流"}]}`, acceptanceModel))
	if response.Status != http.StatusOK {
		t.Fatalf("F5 stream status=%d body=%s", response.Status, response.Body)
	}
	if !strings.Contains(response.ContentType, "text/event-stream") {
		t.Fatalf("F5 content-type=%q", response.ContentType)
	}
	if !strings.Contains(response.Body, "MOCK-STREAM") {
		t.Fatalf("F5 first-chunk delta missing from committed stream: %s", response.Body)
	}
	// 已提交断流不得拼接第二账户：备选账户零命中。
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 0 {
		t.Fatalf("F5 backup account hits=%d want 0（已提交流不允许切号拼接）", got)
	}
	if got := len(f.mock.protocolCallsByKey(streamKey)); got != 1 {
		t.Fatalf("F5 stream account attempts=%d want 1", got)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		// 归因裁决（E2E-FINDING #12，2026-09-17）：Node 归档
		// migration-backup-1/node/final-archive/backend/src/modules/gateway/usage/
		// records.ts:324 失败分支兜底 failureAttribution ?? 'account_upstream'，流式
		// !completed 终态不传归因 → 兜底 account_upstream；Go finalize.go 是精确
		// 镜像非迁移遗漏。F5 断流发起方是上游 mock（模拟上游 TCP reset），非
		// downstream_closed 语义（专属下游客户端断开）。
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			for _, record := range f.listUsageRecords(route.apiKeyID) {
				if !record.Success && record.FailureAttribution == "account_upstream" {
					return
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("usage records not account_upstream failure attribution row in time")
	})
}

// F6 多 Key 池：首 key 401 / 次 key ok → 同账户服务成功。
func fullchainF6(t *testing.T, f *fullchainFixture) {
	firstKey := fullchainUpstreamKey(t, "F6-k1")
	secondKey := fullchainUpstreamKey(t, "F6-k2")
	route := f.newRoute("F6", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{
			key: firstKey,
			extra: map[string]any{
				"credentials": map[string]any{
					"api_key":          firstKey,
					"api_keys":         []any{firstKey, secondKey},
					"api_key_strategy": "failover",
				},
			},
		}}},
	}, nil, nil)
	f.mock.script(firstKey, platformmock.ScenarioStatus401)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F6-Key池"))
	if response.Status != http.StatusOK {
		t.Fatalf("F6 status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKey(firstKey)); got != 1 {
		t.Fatalf("F6 first key hits=%d want 1", got)
	}
	if got := len(f.mock.protocolCallsByKey(secondKey)); got != 1 {
		t.Fatalf("F6 second key hits=%d want 1", got)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			success := 0
			for _, record := range records {
				if record.Success && record.AccountID == route.accountIDs[0] {
					success++
				}
			}
			return success == 1
		}, "single success row on the pooled account")
	})
}

// F7 候选耗尽：组内全 500 → 客户端 503 统一消息；各账户状态不变。
func fullchainF7(t *testing.T, f *fullchainFixture) {
	exhaustA := fullchainUpstreamKey(t, "F7-a")
	exhaustB := fullchainUpstreamKey(t, "F7-b")
	route := f.newRoute("F7", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: exhaustA, defaultScenario: platformmock.ScenarioStatus500},
			{key: exhaustB, defaultScenario: platformmock.ScenarioStatus500},
		}},
	}, nil, nil)

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F7-耗尽"))
	if response.Status != http.StatusServiceUnavailable {
		t.Fatalf("F7 status=%d body=%s", response.Status, response.Body)
	}
	if strings.TrimSpace(response.Body) == "" {
		t.Fatalf("F7 empty error body")
	}
	// 每个账户先同账户重试（≤+2）再切换；断言两个账户都被尝试过。
	if got := len(f.mock.protocolCallsByKey(exhaustA)); got < 2 {
		t.Fatalf("F7 account A attempts=%d want >=2", got)
	}
	if got := len(f.mock.protocolCallsByKey(exhaustB)); got < 2 {
		t.Fatalf("F7 account B attempts=%d want >=2", got)
	}
	for index, accountID := range route.accountIDs {
		snapshot := f.accountSnapshot(accountID)
		if str(snapshot["status"]) != "active" {
			t.Fatalf("F7 account %d status=%v want active（opaque 耗尽不写状态）", index, snapshot["status"])
		}
	}
}

// ---------------------------------------------------------------------------
// G 分组
// ---------------------------------------------------------------------------

// chatE 是并发场景用的 chat 变体：传输错误不 Fatal（goroutine 安全），以
// status=0 + 错误信息返回，供「连接被网关中断」的 canary 判定。
func (f *fullchainFixture) chatE(apiKey, body string) (fullchainChatResponse, error) {
	f.t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.gw.baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return fullchainChatResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fullchainChatResponse{Status: 0, Body: err.Error()}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return fullchainChatResponse{Status: response.StatusCode, Body: err.Error()}, err
	}
	return fullchainChatResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: string(raw)}, nil
}

// G1 高并发分组排队：账户并发 1 + mock 持流，5 并发 → 全部最终成功且出现
// 排队审计（非 503）。
func fullchainG1(t *testing.T, f *fullchainFixture) {
	holdKey := fullchainUpstreamKey(t, "G1-hold")
	route := f.newRoute("G1", "normal", []fullchainGroupSpec{
		{
			groupType:        "high_concurrency",
			schedulingPolicy: map[string]any{"defaultSoftConcurrency": 1},
			accounts: []fullchainAccountSpec{
				{key: holdKey, script: []platformmock.Scenario{fullchainHoldScenario}, extra: map[string]any{"concurrencyLimit": 1}},
			},
		},
	}, nil, nil)

	const parallel = 5
	responses := make([]fullchainChatResponse, parallel)
	errs := make([]error, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			responses[index], errs[index] = f.chatE(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("G1-%d", index)))
		}(i)
	}
	// 持流 1.5s：确保后续请求进入排队路径（高并发组 WaitForCapacity）。
	time.Sleep(1500 * time.Millisecond)
	f.mock.releaseHold(holdKey)
	wg.Wait()

	successes, connectionDropped := 0, 0
	for index := range responses {
		switch {
		case errs[index] == nil && responses[index].Status == http.StatusOK:
			successes++
		case errs[index] != nil:
			// E2E-FINDING #5（真实缺陷，崩溃级）：engine.HighConcurrencyQueue
			// 从未装配（internal/gatewaydispatch/engine.go:90 声明、
			// upstreamdispatch.go:863 / preparation.go:642 直接消费，全仓库
			// 无赋值点）。高并发组并发满进入排队路径时 nil 接口调用 panic，
			// net/http 按连接恢复 → 客户端连接被中断。队列已装配
			//（chain_runtime.go NewHighConcurrencyGroupQueue → compose 适配器
			// → engine.HighConcurrencyQueue），连接中断不再是合法结局。
			connectionDropped++
		default:
			t.Fatalf("G1 request %d unexpected status=%d body=%s", index, responses[index].Status, responses[index].Body)
		}
	}
	if successes < 1 {
		t.Fatalf("G1 no request succeeded; even the concurrency-slot holder failed")
	}
	// E2E-FINDING #13（已修复 2026-09-17）：根因是并发事实源分裂——compose.go
	// 未把 chainServices.ConcurrencyTracker 传给 chainRuntimeDeps，chain_compose.go
	// nil 兜底新建第二个实例给 engine → queue 读数恒 0、立即 Ready、请求永不
	// 入队。已接线同一事实源，连接中断不再是合法结局。
	if connectionDropped > 0 {
		t.Fatalf("G1 queue path dropped %d/%d connections（E2E-FINDING #13 并发事实源接线已修复，连接中断不再是合法结局）", connectionDropped, parallel)
	}
	// 队列生效路径：全部成功后必须有排队审计。
	f.waitAuditLabels(t, route.apiKeyID, []string{"high_concurrency_dispatch_queue"}, "high-concurrency queue event")
}

// G2 普通组并发满：切组内下一账户。
func fullchainG2(t *testing.T, f *fullchainFixture) {
	holdKey := fullchainUpstreamKey(t, "G2-hold")
	route := f.newRoute("G2", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: holdKey, script: []platformmock.Scenario{fullchainHoldScenario}, extra: map[string]any{"concurrencyLimit": 1}},
			{},
		}},
	}, nil, nil)

	firstDone := make(chan fullchainChatResponse, 1)
	go func() {
		firstDone <- f.chat(route.apiKey, chatBody(acceptanceModel, "G2-持流"))
	}()
	time.Sleep(1200 * time.Millisecond)
	second := f.chat(route.apiKey, chatBody(acceptanceModel, "G2-切换"))
	if second.Status != http.StatusOK {
		t.Fatalf("G2 second request status=%d body=%s", second.Status, second.Body)
	}
	f.mock.releaseHold(holdKey)
	first := <-firstDone
	if first.Status != http.StatusOK {
		t.Fatalf("G2 first request status=%d body=%s", first.Status, first.Body)
	}
	if got := len(f.mock.protocolCallsByKey(route.upstreamKeys[1])); got != 1 {
		t.Fatalf("G2 next-account hits=%d want 1", got)
	}

	t.Run("usage_attribution", func(t *testing.T) {
		f.requireUsageChain(t)
		f.waitUsageRecords(t, route.apiKeyID, func(records []fullchainUsageRecord) bool {
			perAccount := map[string]int{}
			for _, record := range records {
				if record.Success {
					perAccount[record.AccountID]++
				}
			}
			return perAccount[route.accountIDs[0]] == 1 && perAccount[route.accountIDs[1]] == 1
		}, "one success row per account")
	})
}

// ---------------------------------------------------------------------------
// Stage 2 矩阵入口
// ---------------------------------------------------------------------------

// TestFullchainStage2Matrix Stage 2 全矩阵（共享一个隔离夹具；子测试名即
// 计划 §3 矩阵编号）。M2（真伪混搭）在 REAL 门控的 fullchain_real_test.go。
func TestFullchainStage2Matrix(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	t.Run("R1_normal_fixed_priority", func(t *testing.T) { fullchainR1(t, f) })
	t.Run("R2_failover_primary_backup", func(t *testing.T) { fullchainR2(t, f) })
	t.Run("R3_round_robin_sequence", func(t *testing.T) { fullchainR3(t, f) })
	t.Run("R4_weighted_2_1", func(t *testing.T) { fullchainR4(t, f) })
	t.Run("R5_speed_first_first_byte_cutover", func(t *testing.T) { fullchainR5(t, f) })
	t.Run("F1_same_account_transient_retry", func(t *testing.T) { fullchainF1(t, f) })
	t.Run("F2_cross_account_switch", func(t *testing.T) { fullchainF2(t, f) })
	t.Run("F3_system_quota_rate_limited", func(t *testing.T) { fullchainF3(t, f) })
	t.Run("F4_explicit_error_disabled", func(t *testing.T) { fullchainF4(t, f) })
	t.Run("F5_committed_stream_no_splice", func(t *testing.T) { fullchainF5(t, f) })
	t.Run("F6_api_key_pool_failover", func(t *testing.T) { fullchainF6(t, f) })
	t.Run("F7_candidates_exhausted_503", func(t *testing.T) { fullchainF7(t, f) })
	t.Run("F8_upstream_content_encoding_none", func(t *testing.T) { fullchainF8(t, f) })
	t.Run("G1_high_concurrency_queue", func(t *testing.T) { fullchainG1(t, f) })
	t.Run("G2_normal_group_concurrency_next_account", func(t *testing.T) { fullchainG2(t, f) })
}

// fullchainF8 E2E-FINDING #8（已修复）：上游返回非标准 `Content-Encoding:
// none`（实测生产上游在真实客户端流式请求下出现，见 C3）时，decode 层把
// none 与空值视同未压缩直通（transport.go parseContentEncodings 归一化为
// identity），真实未知编码仍报 UnsupportedUpstreamResponseEncodingError。
// 本子测试用测试侧响应头注入确定性复现：注入 none 后 chat 必须 200 + MOCK
// 内容；真实未知编码（zstd）仍被拒绝的语义由 transport 单测覆盖。
func fullchainF8(t *testing.T, f *fullchainFixture) {
	t.Helper()
	mockKey := fullchainUpstreamKey(t, "F8")
	route := f.newRoute("F8", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: mockKey, script: []platformmock.Scenario{platformmock.ScenarioChatOK}}}},
	}, nil, nil)
	f.mock.setResponseHeaderOverride("Content-Encoding", "none")
	defer f.mock.clearResponseHeaderOverrides()

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "F8"))
	if f.mock.injectedHeaderCallCount() == 0 {
		t.Fatalf("F8 canary 注入头未生效（上游调用 %d 次）", len(f.mock.callsByKey(mockKey)))
	}
	if response.Status != http.StatusOK {
		t.Fatalf("E2E-FINDING #8 已修复：none 应视同未压缩直通（chat 200 + MOCK 内容），"+
			"实际 status=%d body=%s", response.Status, maskRealBody(response.Body))
	}
}
