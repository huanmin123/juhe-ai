package modelcheckprobe

// suite_hooks_test.go 覆盖逐探针回调契约：每个带 ItemKey 标签的真实探针
// 请求各触发一次 OnProbeStarted / OnProbeCompleted（成对、有序、字段真实），
// 套件 Prefix 决定 itemKey 作用域，回调缺失时执行结果与既有路径逐字节一致。

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// TestWProbeHooksFirePerProbeInOrder：quick 全成功 run 的探针时序与载荷。
func TestWProbeHooksFirePerProbeInOrder(t *testing.T) {
	server := w9fScriptedServer(t, []int{0})
	defer server.Close()
	type hookRecord struct {
		kind    string
		payload ProbeHook
	}
	var sequence []hookRecord
	suite := w9fSuite(server.URL, "quick")
	suite.RequestModel = "gpt-5.6-public"
	suite.ModelMappingApplied = true
	suite.OnProbeStarted = func(hook ProbeHook) {
		sequence = append(sequence, hookRecord{kind: "started", payload: hook})
	}
	suite.OnProbeCompleted = func(hook ProbeHook) {
		sequence = append(sequence, hookRecord{kind: "completed", payload: hook})
	}
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sequence) == 0 || len(sequence)%2 != 0 {
		t.Fatalf("hook 序列必须成对: %d", len(sequence))
	}
	for index := 0; index < len(sequence); index += 2 {
		start, completed := sequence[index], sequence[index+1]
		if start.kind != "started" || completed.kind != "completed" {
			t.Fatalf("hook 顺序错误 @%d: %s/%s", index, start.kind, completed.kind)
		}
		if start.payload.ItemKey == "" || start.payload.ItemKey != completed.payload.ItemKey {
			t.Fatalf("started/completed itemKey 不成对: %q vs %q", start.payload.ItemKey, completed.payload.ItemKey)
		}
	}
	startedKeys := make([]string, 0, len(sequence)/2)
	for index := 0; index < len(sequence); index += 2 {
		startedKeys = append(startedKeys, sequence[index].payload.ItemKey)
	}
	expected := []string{"protocol_basic", "structured_output", "tool_calling", "identity_selfreport", "identity_selfreport", "identity_extraction", "identity_extraction", "identity_anchor"}
	if !reflect.DeepEqual(startedKeys, expected) {
		t.Fatalf("started itemKeys=%v 期望=%v", startedKeys, expected)
	}
	for index := 0; index < len(sequence); index += 2 {
		hook := sequence[index].payload
		if hook.Method != http.MethodPost || hook.Path != "/v1/responses" {
			t.Fatalf("started 方法/路径=%q %q", hook.Method, hook.Path)
		}
		if hook.ExpectedModel != "gpt-5.6-sol" || hook.UpstreamModel != "gpt-5.6-sol" || hook.RequestModel != "gpt-5.6-public" || !hook.ModelMappingApplied {
			t.Fatalf("started 模型字段=%+v", hook)
		}
		completed := sequence[index+1].payload
		if completed.StatusCode != http.StatusOK || !completed.Success {
			t.Fatalf("completed 状态=%+v", completed)
		}
		if completed.ResponseModel != "gpt-5.6-sol" {
			t.Fatalf("completed 结果字段=%+v", completed)
		}
		if completed.DurationMS < 0 {
			t.Fatalf("completed 耗时=%d", completed.DurationMS)
		}
	}
	// 输出预览只在首个（脚本内容确定的）探针上断言；身份族题面每轮随机，
	// 其完成文本不固定。
	if sequence[1].payload.OutputPreview != "OK-MODEL-CHECK" {
		t.Fatalf("completed 输出预览=%q", sequence[1].payload.OutputPreview)
	}
	if len(items) == 0 {
		t.Fatal("hooks 不得改变返回项")
	}
}

// TestWProbeHooksNilKeepItemsIdentical：回调缺失与回调存在两次执行的结果
// 必须逐字节一致（hooks 只观察，不改变执行路径）。空 Profile 只跑确定性
// 核心探针，避免身份族随机题面造成的跨运行内容差异。
func TestWProbeHooksNilKeepItemsIdentical(t *testing.T) {
	server := w9fScriptedServer(t, []int{0})
	defer server.Close()
	plain, err := RunSuite(context.Background(), w9fSuite(server.URL, ""), time.Second)
	if err != nil {
		t.Fatalf("无回调 run: %v", err)
	}
	hookedSuite := w9fSuite(server.URL, "")
	hookedSuite.OnProbeStarted = func(ProbeHook) {}
	hookedSuite.OnProbeCompleted = func(ProbeHook) {}
	hooked, err := RunSuite(context.Background(), hookedSuite, time.Second)
	if err != nil {
		t.Fatalf("带回调 run: %v", err)
	}
	plainJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	hookedJSON, err := json.Marshal(hooked)
	if err != nil {
		t.Fatal(err)
	}
	if string(plainJSON) != string(hookedJSON) {
		t.Fatalf("回调不得改变结果: 无回调=%s 带回调=%s", plainJSON, hookedJSON)
	}
}

// TestWProbeHooksScopedBySuitePrefix：非空 Prefix 套件（可信对比嵌套形态）
// 的 itemKey 自动带作用域前缀，已带点的标签保持原样。
func TestWProbeHooksScopedBySuitePrefix(t *testing.T) {
	server := w9fScriptedServer(t, []int{0})
	defer server.Close()
	var scopedKeys []string
	suite := w9fSuite(server.URL, "quick")
	suite.Prefix = "trusted_comparison"
	suite.OnProbeStarted = func(hook ProbeHook) {
		scopedKeys = append(scopedKeys, hook.ItemKey)
	}
	if _, err := RunSuite(context.Background(), suite, time.Second); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(scopedKeys) == 0 {
		t.Fatal("必须触发 started hooks")
	}
	for _, itemKey := range scopedKeys {
		if len(itemKey) <= len("trusted_comparison.") || itemKey[:len("trusted_comparison.")] != "trusted_comparison." {
			t.Fatalf("itemKey 缺作用域前缀: %q", itemKey)
		}
	}
	if scopedKeys[0] != "trusted_comparison.protocol_basic" {
		t.Fatalf("首个 itemKey=%q", scopedKeys[0])
	}
}

// TestWProbeOutputPreviewBounds：输出预览必须截断，避免超长完成文本进入
// 进度事件。
func TestWProbeOutputPreviewBounds(t *testing.T) {
	long := make([]rune, 200)
	for index := range long {
		long[index] = '字'
	}
	if got := probeOutputPreview(string(long)); len([]rune(got)) != 120 {
		t.Fatalf("预览长度=%d", len([]rune(got)))
	}
	if got := probeOutputPreview("  OK  "); got != "OK" {
		t.Fatalf("预览修剪=%q", got)
	}
}

// TestWScopedItemKey：前缀作用域规则与 scopeEvaluation 一致。
func TestWScopedItemKey(t *testing.T) {
	if got := scopedItemKey("", "protocol_basic"); got != "protocol_basic" {
		t.Fatalf("空前缀=%q", got)
	}
	if got := scopedItemKey("trusted_comparison", "protocol_basic"); got != "trusted_comparison.protocol_basic" {
		t.Fatalf("带前缀=%q", got)
	}
	if got := scopedItemKey("trusted_comparison", "trusted_comparison.comparison"); got != "trusted_comparison.comparison" {
		t.Fatalf("已带点标签必须保持原样=%q", got)
	}
}
