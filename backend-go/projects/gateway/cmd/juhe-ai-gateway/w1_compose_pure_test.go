package main

// w1_compose_pure_test.go —— chain_compose.go / compose.go /
// chain_accounts_secret.go 纯 helper 的单元测试（w1c 前缀，TestW1C 入口）。
// 全部用例确定性可重放：不依赖网络、数据库、磁盘与并发时序；外部协作
// （时钟 / settings store / runtime cache 读模型 / hybrid 端口）一律注入
// 本文件内定义的 fake。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/settings"
	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// ---------------------------------------------------------------------------
// fakes 与小工具
// ---------------------------------------------------------------------------

// w1cFixedClock 是 gatewaypreauth.Clock 的固定时钟 fake。
type w1cFixedClock struct{ now time.Time }

func (c w1cFixedClock) Now() time.Time { return c.now }

// w1cFakeSharedJSONCache 实现 gatewayhybrid.SharedJSONCache。
type w1cFakeSharedJSONCache struct{}

func (w1cFakeSharedJSONCache) Get(context.Context, string) (*gatewayhybrid.HybridScoringCacheEntry, error) {
	return nil, nil
}

func (w1cFakeSharedJSONCache) Set(context.Context, string, gatewayhybrid.HybridScoringCacheEntry, int64) error {
	return nil
}

func (w1cFakeSharedJSONCache) Clear(context.Context) error { return nil }

// w1cFakeRuntimeStateStore 实现 gatewayhybrid.RuntimeStateStore。
type w1cFakeRuntimeStateStore struct{}

func (w1cFakeRuntimeStateStore) GetJSON(context.Context, string, any) (bool, error) {
	return false, nil
}

func (w1cFakeRuntimeStateStore) SetJSON(context.Context, string, any, int64) error { return nil }

// w1cFakeAuxiliaryDispatcher 实现 gatewayhybrid.AuxiliaryDispatcher。
type w1cFakeAuxiliaryDispatcher struct{}

func (w1cFakeAuxiliaryDispatcher) DispatchHybridAuxiliaryChatCompletion(context.Context, gatewayhybrid.AuxiliaryDispatchInput) (gatewayhybrid.AuxiliaryDispatchSuccess, *gatewayhybrid.AuxiliaryDispatchFailure) {
	return gatewayhybrid.AuxiliaryDispatchSuccess{}, nil
}

// w1cFakeUsageRecorder 实现 gatewayhybrid.UsageRecorder。
type w1cFakeUsageRecorder struct{}

func (w1cFakeUsageRecorder) RecordHybridScoringAttempt(context.Context, gatewayhybrid.ScoringAttemptRecord) error {
	return nil
}

// w1cFakeRouteDiagnostics 实现 gatewayhybrid.RouteDiagnosticsPublisher。
type w1cFakeRouteDiagnostics struct{}

func (w1cFakeRouteDiagnostics) PublishHybridRouteDecision(*gatewayhybrid.OrderedJSON) {}

// w1cErrDriver / w1cErrConnector 让任何数据库访问都按固定错误失败（settings
// store 错误透传路径用；不起真实数据库）。
type w1cErrDriver struct{}

func (w1cErrDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w1c fake 数据库不可用")
}

type w1cErrConnector struct{}

func (w1cErrConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("w1c fake 数据库不可用")
}

func (w1cErrConnector) Driver() driver.Driver { return w1cErrDriver{} }

// w1cFailingReadModels 只覆写 ReadGatewaySettings；其余方法内嵌接口保持
// 未实现（构造路径不会调用它们）。
type w1cFailingReadModels struct{ gatewayruntimecache.ReadModels }

func (w1cFailingReadModels) ReadGatewaySettings(context.Context) (gatewayruntimecache.GatewaySettings, error) {
	return gatewayruntimecache.GatewaySettings{}, errors.New("w1c fake settings 读取失败")
}

func w1cStringPtr(value string) *string { return &value }

func w1cBoolPtr(value bool) *bool { return &value }

func w1cFloat64Ptr(value float64) *float64 { return &value }

func w1cInt64Ptr(value int64) *int64 { return &value }

// ---------------------------------------------------------------------------
// chain_compose.go：gatewaybodyLogger
// ---------------------------------------------------------------------------

// 编译期断言：gatewaybodyLogger 满足 gatewaybody.Logger 端口。
var _ gatewaybody.Logger = gatewaybodyLogger{}

func TestW1CGatewaybodyLoggerMethods(t *testing.T) {
	cases := []struct {
		name  string
		call  func(gatewaybodyLogger, string, map[string]any)
		level string
	}{
		{"Debug", gatewaybodyLogger.Debug, "DEBUG"},
		{"Info", gatewaybodyLogger.Info, "INFO"},
		{"Warn", gatewaybodyLogger.Warn, "WARN"},
		{"Error", gatewaybodyLogger.Error, "ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			tc.call(gatewaybodyLogger{inner: logger}, "w1c-消息", map[string]any{"w1c键": "值"})
			out := buf.String()
			if !strings.Contains(out, "level="+tc.level) {
				t.Errorf("输出缺少级别 %s，实际：%s", tc.level, out)
			}
			if !strings.Contains(out, "w1c-消息") {
				t.Errorf("输出缺少消息文本，实际：%s", out)
			}
			if !strings.Contains(out, "w1c键=值") {
				t.Errorf("输出缺少 fields 键值对，实际：%s", out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：hybrid *Of 适配器
// ---------------------------------------------------------------------------

func TestW1CHybridAdapterOfHelpers(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := hybridClockOf(w1cFixedClock{now: now}); !got().Equal(now) {
		t.Fatalf("hybridClockOf 应透传时钟 Now，实际 %v", got())
	}

	if got := hybridSharedCacheOf(nil); got != nil {
		t.Fatalf("hybridSharedCacheOf(nil) 应返回 nil，实际 %#v", got)
	}
	fakeCache := &w1cFakeSharedJSONCache{}
	if got := hybridSharedCacheOf(fakeCache); got != gatewayhybrid.SharedJSONCache(fakeCache) {
		t.Fatalf("hybridSharedCacheOf 应透传非 nil 实现，实际 %#v", got)
	}

	if got := hybridRuntimeStateOf(nil); got != nil {
		t.Fatalf("hybridRuntimeStateOf(nil) 应返回 nil，实际 %#v", got)
	}
	fakeState := &w1cFakeRuntimeStateStore{}
	if got := hybridRuntimeStateOf(fakeState); got != gatewayhybrid.RuntimeStateStore(fakeState) {
		t.Fatalf("hybridRuntimeStateOf 应透传非 nil 实现，实际 %#v", got)
	}

	if got := hybridAuxiliaryOf(nil); got != nil {
		t.Fatalf("hybridAuxiliaryOf(nil) 应返回 nil，实际 %#v", got)
	}
	fakeDispatcher := &w1cFakeAuxiliaryDispatcher{}
	if got := hybridAuxiliaryOf(fakeDispatcher); got != gatewayhybrid.AuxiliaryDispatcher(fakeDispatcher) {
		t.Fatalf("hybridAuxiliaryOf 应透传非 nil 实现，实际 %#v", got)
	}

	if got := hybridUsageRecorderOf(nil); got != nil {
		t.Fatalf("hybridUsageRecorderOf(nil) 应返回 nil，实际 %#v", got)
	}
	fakeRecorder := &w1cFakeUsageRecorder{}
	if got := hybridUsageRecorderOf(fakeRecorder); got != gatewayhybrid.UsageRecorder(fakeRecorder) {
		t.Fatalf("hybridUsageRecorderOf 应透传非 nil 实现，实际 %#v", got)
	}

	if got := hybridDiagnosticsOf(nil); got != nil {
		t.Fatalf("hybridDiagnosticsOf(nil) 应返回 nil，实际 %#v", got)
	}
	fakeDiagnostics := &w1cFakeRouteDiagnostics{}
	if got := hybridDiagnosticsOf(fakeDiagnostics); got != gatewayhybrid.RouteDiagnosticsPublisher(fakeDiagnostics) {
		t.Fatalf("hybridDiagnosticsOf 应透传非 nil 实现，实际 %#v", got)
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：auxiliaryDispatchFailure / firstNonEmptyString
// ---------------------------------------------------------------------------

func TestW1CAuxiliaryDispatchFailureArms(t *testing.T) {
	input := gatewayhybrid.AuxiliaryDispatchInput{
		DispatchErrorCode:     "dispatch-code",
		DispatchErrorMessage:  "dispatch-msg",
		NoAccountErrorCode:    "no-account-code",
		NoAccountErrorMessage: "no-account-msg",
		TargetModel:           "gpt-x",
	}
	cases := []struct {
		name              string
		errorCode         string
		errorMessage      string
		account           *gatewayhybrid.OpenAIAccountSecret
		groupID           string
		hasGroupID        bool
		statusCode        int
		hasStatusCode     bool
		shouldRecordUsage bool
	}{
		{"nil账户无分组", "no-account-code", "no-account-msg", nil, "", false, 0, false, false},
		{"带账户分组与状态码", "dispatch-code", "upstream失败", &gatewayhybrid.OpenAIAccountSecret{ID: "acc-1"}, "grp-1", true, 502, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			success, failure := auxiliaryDispatchFailure(input, tc.errorCode, tc.errorMessage, tc.account, tc.groupID, tc.hasGroupID, tc.statusCode, tc.hasStatusCode, tc.shouldRecordUsage)
			if success.GroupID != "" || success.StatusCode != 0 || len(success.ResponseBody) != 0 || success.Finish != nil {
				t.Fatalf("成功臂应保持零值，实际 GroupID=%q StatusCode=%d BodyLen=%d FinishNil=%v", success.GroupID, success.StatusCode, len(success.ResponseBody), success.Finish == nil)
			}
			if success.Account != (gatewayhybrid.OpenAIAccountSecret{}) {
				t.Fatalf("成功臂账户应为零值，实际 %+v", success.Account)
			}
			if failure == nil {
				t.Fatalf("失败臂不应为 nil")
			}
			if failure.ErrorCode != tc.errorCode {
				t.Errorf("ErrorCode 不符，期望 %q 实际 %q", tc.errorCode, failure.ErrorCode)
			}
			if failure.ErrorMessage != tc.errorMessage {
				t.Errorf("ErrorMessage 不符，期望 %q 实际 %q", tc.errorMessage, failure.ErrorMessage)
			}
			if failure.GroupID != tc.groupID || failure.HasGroupID != tc.hasGroupID {
				t.Errorf("分组透传不符，期望 (%q,%v) 实际 (%q,%v)", tc.groupID, tc.hasGroupID, failure.GroupID, failure.HasGroupID)
			}
			if failure.StatusCode != tc.statusCode || failure.HasStatusCode != tc.hasStatusCode {
				t.Errorf("状态码透传不符，期望 (%d,%v) 实际 (%d,%v)", tc.statusCode, tc.hasStatusCode, failure.StatusCode, failure.HasStatusCode)
			}
			if failure.ShouldRecordUsage != tc.shouldRecordUsage {
				t.Errorf("ShouldRecordUsage 不符，期望 %v 实际 %v", tc.shouldRecordUsage, failure.ShouldRecordUsage)
			}
			if (failure.Account != nil) != (tc.account != nil) {
				t.Fatalf("账户指针透传不符，期望 nil=%v 实际 nil=%v", tc.account == nil, failure.Account == nil)
			}
			if failure.Account != nil && failure.Account.ID != tc.account.ID {
				t.Errorf("账户 ID 透传不符，期望 %q 实际 %q", tc.account.ID, failure.Account.ID)
			}
		})
	}
}

func TestW1CFirstNonEmptyString(t *testing.T) {
	cases := []struct {
		name  string
		value []string
		want  string
	}{
		{"全空", []string{"", "  ", ""}, ""},
		{"无参数", nil, ""},
		{"跳过空白取首个非空", []string{"", " ", "a", "b"}, "a"},
		{"首个即非空", []string{"x", "y"}, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmptyString(tc.value...); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：resolveAuxiliaryAccountModelMapping
// ---------------------------------------------------------------------------

func TestW1CResolveAuxiliaryAccountModelMapping(t *testing.T) {
	sameFamily := gatewayruntimecache.AccountModelMapping{
		SourceModel:            "gpt-4o",
		SourceEndpointFamily:   "chat_completions",
		UpstreamModel:          "gpt-4o-2024-11-20",
		UpstreamEndpointFamily: "chat_completions",
		Enabled:                true,
	}
	disabled := sameFamily
	disabled.Enabled = false
	identity := sameFamily
	identity.UpstreamModel = "gpt-4o"
	explicitRoute := sameFamily
	explicitRoute.RuntimeSource = w1cStringPtr(gatewayopenai.RuntimeSourceExplicitHybridRoute)
	crossAnthropic := gatewayruntimecache.AccountModelMapping{
		SourceModel:            "gpt-x",
		SourceEndpointFamily:   "chat_completions",
		UpstreamModel:          "claude-x",
		UpstreamEndpointFamily: "messages",
		Enabled:                true,
	}
	responsesSource := gatewayruntimecache.AccountModelMapping{
		SourceModel:            "gpt-x",
		SourceEndpointFamily:   "responses",
		UpstreamModel:          "chat-x",
		UpstreamEndpointFamily: "chat_completions",
		Enabled:                true,
	}
	cases := []struct {
		name               string
		account            gatewayruntimecache.OpenAIAccountSecret
		target             string
		wantNil            bool
		wantSource         string
		wantUpstream       string
		wantSourceFamily   string
		wantUpstreamFamily string
	}{
		{"目标模型为空", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{sameFamily}}, "", true, "", "", "", ""},
		{"无映射", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown"}, "gpt-4o", true, "", "", "", ""},
		{"同族改名", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{sameFamily}}, "gpt-4o", false, "gpt-4o", "gpt-4o-2024-11-20", "chat_completions", "chat_completions"},
		{"映射禁用", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{disabled}}, "gpt-4o", true, "", "", "", ""},
		{"显式混合路由来源跳过", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{explicitRoute}}, "gpt-4o", true, "", "", "", ""},
		{"恒等映射跳过", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{identity}}, "gpt-4o", true, "", "", "", ""},
		{"目标模型无匹配", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{sameFamily}}, "other-model", true, "", "", "", ""},
		{"跨协议混合供应商支持", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "Hybrid", ModelMappings: []gatewayruntimecache.AccountModelMapping{crossAnthropic}}, "gpt-x", false, "gpt-x", "claude-x", "chat_completions", "messages"},
		{"跨协议普通供应商不支持", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "unknown", ModelMappings: []gatewayruntimecache.AccountModelMapping{crossAnthropic}}, "gpt-x", true, "", "", "", ""},
		{"源协议族不匹配", gatewayruntimecache.OpenAIAccountSecret{ProviderCode: "Hybrid", ModelMappings: []gatewayruntimecache.AccountModelMapping{responsesSource}}, "gpt-x", true, "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAuxiliaryAccountModelMapping(tc.account, tc.target)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("应解析为 nil，实际 %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("不应解析为 nil")
			}
			if got.SourceModel != tc.wantSource || got.UpstreamModel != tc.wantUpstream ||
				got.SourceEndpointFamily != tc.wantSourceFamily || got.UpstreamEndpointFamily != tc.wantUpstreamFamily {
				t.Fatalf("映射字段不符，期望 (%s,%s,%s,%s) 实际 (%s,%s,%s,%s)",
					tc.wantSource, tc.wantUpstream, tc.wantSourceFamily, tc.wantUpstreamFamily,
					got.SourceModel, got.UpstreamModel, got.SourceEndpointFamily, got.UpstreamEndpointFamily)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：客户端模型目录
// ---------------------------------------------------------------------------

func TestW1CSortedUniqueProviderCodes(t *testing.T) {
	cases := []struct {
		name    string
		input   []string
		want    []string
		wantNil bool
	}{
		{"去重归一排序", []string{" OpenAI", "openai", "GLM", "", "DeepSeek ", "glm"}, []string{"deepseek", "glm", "openai"}, false},
		{"空输入", nil, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sortedUniqueProviderCodes(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("期望 %#v 实际 %#v", tc.want, got)
			}
		})
	}
}

func TestW1CSelectClientCatalogItems(t *testing.T) {
	items := []gatewayruntimecache.ProviderModelCatalogItem{
		// G：与 B 同模型但 scope 更低（去重时被 B 淘汰）。
		{Model: "alpha", Scope: "built_in", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-06-01"), InputUsdPer1M: w1cFloat64Ptr(2)},
		{Model: "alpha", Scope: "global", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-06-01"), InputUsdPer1M: w1cFloat64Ptr(2)},
		{Model: "zeta", Scope: "personal", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-01-01"), InputUsdPer1M: w1cFloat64Ptr(1)},
		{Model: "mike", Scope: "built_in", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2024-01-01"), InputUsdPer1M: w1cFloat64Ptr(3)},
		// D：非 active 应剔除。
		{Model: "bravo", Scope: "built_in", Status: "retired", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-01-01"), InputUsdPer1M: w1cFloat64Ptr(1)},
		// E：built_in 且目录不可见应剔除。
		{Model: "kilo", Scope: "built_in", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-01-01"), InputUsdPer1M: w1cFloat64Ptr(1),
			CatalogVisible: w1cBoolPtr(false)},
		// F：无可见价格应剔除。
		{Model: "november", Scope: "built_in", Status: "active", ProviderCode: "openai",
			ReleaseDate: w1cStringPtr("2025-01-01")},
	}
	selected := selectClientCatalogItems(items)
	gotModels := make([]string, 0, len(selected))
	for _, item := range selected {
		gotModels = append(gotModels, item.Model)
	}
	want := []string{"alpha", "zeta", "mike"}
	if !reflect.DeepEqual(gotModels, want) {
		t.Fatalf("选择与排序不符，期望 %v 实际 %v", want, gotModels)
	}
}

func TestW1CClientCatalogScopeRank(t *testing.T) {
	cases := []struct {
		scope string
		want  int
	}{
		{"personal", 3},
		{"global", 2},
		{"built_in", 1},
		{"", 1},
		{"other", 1},
	}
	for _, tc := range cases {
		item := gatewayruntimecache.ProviderModelCatalogItem{Scope: tc.scope}
		if got := clientCatalogScopeRank(item); got != tc.want {
			t.Fatalf("scope %q 排名期望 %d 实际 %d", tc.scope, tc.want, got)
		}
	}
}

func TestW1CClientCatalogCompareItemsAndReleaseDate(t *testing.T) {
	newer := gatewayruntimecache.ProviderModelCatalogItem{ReleaseDate: w1cStringPtr("2025-02-01"), ProviderCode: "openai", Model: "m"}
	older := gatewayruntimecache.ProviderModelCatalogItem{ReleaseDate: w1cStringPtr("2024-12-01"), ProviderCode: "openai", Model: "m"}
	if !clientCatalogCompareItems(newer, older) {
		t.Fatalf("新发布日期应排在前（less(newer,older)=true）")
	}
	if clientCatalogCompareItems(older, newer) {
		t.Fatalf("旧发布日期不应排在新日期之前")
	}
	anthropic := gatewayruntimecache.ProviderModelCatalogItem{ReleaseDate: w1cStringPtr("2025-01-01"), ProviderCode: "Anthropic", Model: "m"}
	openai := gatewayruntimecache.ProviderModelCatalogItem{ReleaseDate: w1cStringPtr("2025-01-01"), ProviderCode: "OpenAI", Model: "m"}
	if !clientCatalogCompareItems(anthropic, openai) {
		t.Fatalf("同日期按归一化供应商码升序，anthropic 应在 openai 之前")
	}
	modelA := gatewayruntimecache.ProviderModelCatalogItem{ProviderCode: "openai", Model: "a"}
	modelB := gatewayruntimecache.ProviderModelCatalogItem{ProviderCode: "openai", Model: "b"}
	if !clientCatalogCompareItems(modelA, modelB) {
		t.Fatalf("同日期同供应商按模型名升序")
	}
	if clientCatalogCompareItems(modelB, modelA) {
		t.Fatalf("模型名降序不应判为 less")
	}
	if got := clientCatalogReleaseDate(gatewayruntimecache.ProviderModelCatalogItem{}); got != "" {
		t.Fatalf("nil 日期应为空串，实际 %q", got)
	}
	dated := gatewayruntimecache.ProviderModelCatalogItem{ReleaseDate: w1cStringPtr(" 2025-01-01 ")}
	if got := clientCatalogReleaseDate(dated); got != "2025-01-01" {
		t.Fatalf("日期应去首尾空白，实际 %q", got)
	}
}

func TestW1CClientCatalogHasVisiblePrice(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*gatewayruntimecache.ProviderModelCatalogItem)
		want   bool
	}{
		{"全空价格", func(*gatewayruntimecache.ProviderModelCatalogItem) {}, false},
		{"输入价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.InputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"输出价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.OutputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"缓存读价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.CachedInputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"缓存写价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.CacheWriteUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"图像输入价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.ImageInputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"图像输出价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.ImageOutputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"音频输出价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.AudioOutputUsdPer1M = w1cFloat64Ptr(1) }, true},
		{"单张图像价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) { item.OutputUsdPerImage = w1cFloat64Ptr(1) }, true},
		{"分层价格", func(item *gatewayruntimecache.ProviderModelCatalogItem) {
			item.ServiceTierPrices = json.RawMessage(`[{"x":1}]`)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := &gatewayruntimecache.ProviderModelCatalogItem{}
			tc.mutate(item)
			if got := clientCatalogHasVisiblePrice(*item); got != tc.want {
				t.Fatalf("期望 %v 实际 %v", tc.want, got)
			}
		})
	}
}

func TestW1CClientCatalogEntryOf(t *testing.T) {
	item := gatewayruntimecache.ProviderModelCatalogItem{
		Model:                         "m1",
		Scope:                         "global",
		ReleaseDate:                   w1cStringPtr("2025-01-01"),
		ContextWindowTokens:           w1cInt64Ptr(128),
		SupportedServiceTiers:         []string{"priority", "default"},
		CodexSupportedReasoningLevels: json.RawMessage(`["low","high"]`),
		CodexDefaultReasoningLevel:    json.RawMessage(`"medium"`),
		CodexMultiAgentVersion:        w1cStringPtr("v2"),
	}
	item.CreatedAt = w1cStringPtr("2025-01-02T00:00:00Z")
	item.CapabilityNotes = w1cStringPtr("能力备注")
	item.PricingNotes = w1cStringPtr("价格备注")
	item.Notes = w1cStringPtr("通用备注")
	want := gatewayresponse.ModelCatalogEntry{
		Model:                         "m1",
		Scope:                         "global",
		ReleaseDate:                   "2025-01-01",
		CreatedAt:                     "2025-01-02T00:00:00Z",
		CapabilityNotes:               "能力备注",
		PricingNotes:                  "价格备注",
		Notes:                         "通用备注",
		ContextWindowTokens:           128,
		SupportedServiceTiers:         []string{"priority", "default"},
		CodexSupportedReasoningLevels: []string{"low", "high"},
		CodexDefaultReasoningLevel:    "medium",
		CodexMultiAgentVersion:        "v2",
	}
	if got := clientCatalogEntryOf(item); !reflect.DeepEqual(got, want) {
		t.Fatalf("条目投影不符，期望 %#v 实际 %#v", want, got)
	}
	empty := clientCatalogEntryOf(gatewayruntimecache.ProviderModelCatalogItem{})
	zero := gatewayresponse.ModelCatalogEntry{}
	if !reflect.DeepEqual(empty, zero) {
		t.Fatalf("零值条目投影应保持零值，实际 %#v", empty)
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：nilString / nilInt / rawMessage*
// ---------------------------------------------------------------------------

func TestW1CNilStringNilIntHelpers(t *testing.T) {
	if got := nilString(nil); got != "" {
		t.Fatalf("nilString(nil) 期望空串，实际 %q", got)
	}
	if got := nilString(w1cStringPtr("v")); got != "v" {
		t.Fatalf("nilString 指针值期望 \"v\"，实际 %q", got)
	}
	if got := nilInt(nil); got != 0 {
		t.Fatalf("nilInt(nil) 期望 0，实际 %d", got)
	}
	if got := nilInt(w1cInt64Ptr(7)); got != 7 {
		t.Fatalf("nilInt(7) 期望 7，实际 %d", got)
	}
	if got := nilInt(w1cInt64Ptr(-3)); got != -3 {
		t.Fatalf("nilInt(-3) 期望 -3，实际 %d", got)
	}
}

func TestW1CRawMessageStringHelpers(t *testing.T) {
	listCases := []struct {
		name    string
		raw     json.RawMessage
		want    []string
		wantNil bool
	}{
		{"空输入", nil, nil, true},
		{"字符串数组", json.RawMessage(`["a","b"]`), []string{"a", "b"}, false},
		{"非数组JSON", json.RawMessage(`"text"`), nil, true},
		{"对象JSON", json.RawMessage(`{"a":1}`), nil, true},
		{"数字JSON", json.RawMessage("123"), nil, true},
		{"null输入", json.RawMessage("null"), nil, true},
	}
	for _, tc := range listCases {
		t.Run("列表_"+tc.name, func(t *testing.T) {
			got := rawMessageStringList(tc.raw)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("期望 nil，实际 %#v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("期望 %#v 实际 %#v", tc.want, got)
			}
		})
	}
	stringCases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"空输入", nil, ""},
		{"JSON字符串", json.RawMessage(`"abc"`), "abc"},
		{"数组输入", json.RawMessage(`[1,2]`), ""},
		{"对象输入", json.RawMessage("{}"), ""},
		{"数字输入", json.RawMessage("42"), ""},
		{"null输入", json.RawMessage("null"), ""},
	}
	for _, tc := range stringCases {
		t.Run("标量_"+tc.name, func(t *testing.T) {
			if got := rawMessageString(tc.raw); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：请求快照与运行时快照读取
// ---------------------------------------------------------------------------

func TestW1CPathWithoutQueryOf(t *testing.T) {
	cases := []struct {
		name string
		u    url.URL
		want string
	}{
		{"路径直通", url.URL{Path: "/v1/models", RawQuery: "a=1"}, "/v1/models"},
		{"空路径回退RequestURI", url.URL{RawQuery: "a=1"}, "/"},
		{"纯路径", url.URL{Path: "/health"}, "/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{URL: &tc.u}
			if got := pathWithoutQueryOf(req); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

func TestW1CChainGatewayRuntimeOf(t *testing.T) {
	if got := chainGatewayRuntimeOf(nil); got != nil {
		t.Fatalf("nil 请求应返回 nil，实际 %#v", got)
	}
	plain := (&http.Request{}).WithContext(context.Background())
	if got := chainGatewayRuntimeOf(plain); got != nil {
		t.Fatalf("无快照请求应返回 nil，实际 %#v", got)
	}
	runtime := &gatewayruntimecache.GatewayRuntime{}
	withValue := plain.WithContext(context.WithValue(context.Background(), chainGatewayRuntimeKey{}, runtime))
	if got := chainGatewayRuntimeOf(withValue); got != runtime {
		t.Fatalf("应返回注入的快照指针，实际 %#v", got)
	}
	wrongType := plain.WithContext(context.WithValue(context.Background(), chainGatewayRuntimeKey{}, "runtime"))
	if got := chainGatewayRuntimeOf(wrongType); got != nil {
		t.Fatalf("类型不符应返回 nil，实际 %#v", got)
	}
}

func TestW1CChainTextRawBodyLimitOf(t *testing.T) {
	nilProvider := chainTextRawBodyLimitOf(nil)
	if value, ok := nilProvider(); ok || value != 0 {
		t.Fatalf("nil cache 应保持 unconfigured，实际 value=%d ok=%v", value, ok)
	}
	service, err := gatewayruntimecache.New(w1cFailingReadModels{}, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatalf("构造 runtime cache 失败: %v", err)
	}
	failingProvider := chainTextRawBodyLimitOf(service)
	if value, ok := failingProvider(); ok || value != 0 {
		t.Fatalf("设置读取失败应回落 unconfigured，实际 value=%d ok=%v", value, ok)
	}
}

func TestW1CChainNewAuditID(t *testing.T) {
	clock := w1cFixedClock{now: time.UnixMilli(1720000000000)}
	id := chainNewAuditID(clock)
	pattern := regexp.MustCompile(`^audit_1720000000000_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(id) {
		t.Fatalf("审计 ID 形状不符（audit_毫秒_UUIDv4）：%q", id)
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：body 拒绝记录器（协作者缺席时保持惰性）
// ---------------------------------------------------------------------------

func TestW1CChainBodyRejectionRecorderInertWithoutCollaborators(t *testing.T) {
	recorder := &chainBodyRejectionRecorder{
		audit:        nil,
		auditEnabled: func() bool { return true },
		usage:        nil,
		clock:        w1cFixedClock{now: time.UnixMilli(1720000000000)},
	}
	req := (&http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Path: "/v1/chat/completions", RawQuery: "trace=1"},
	}).WithContext(context.Background())
	// 审计未装配 + 无 runtime 快照：记录应全程惰性，不 panic 也不改变响应。
	recorder.RecordGatewayBodyRejection(req, nil, gatewaybody.RejectionInput{
		StatusCode:   413,
		RawBodyBytes: 1024,
		Reason:       "payload_too_large",
		ErrorCode:    "payload_too_large",
		ErrorMessage: "请求体过大",
		LimitBytes:   16 * 1024 * 1024,
		LimitScope:   "raw_body",
	})
}

// ---------------------------------------------------------------------------
// compose.go：settingsString / settingsValueReader
// ---------------------------------------------------------------------------

func TestW1CSettingsString(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, ""},
		{"字符串", "v1", "v1"},
		{"浮点数", float64(2.5), "2.5"},
		{"整值浮点", float64(3), "3"},
		{"布尔真", true, "true"},
		{"布尔假", false, "false"},
		{"默认int", 42, "42"},
		{"默认切片", []any{"a", "b"}, "[a b]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settingsString(tc.value); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

func TestW1CSettingsValueReaderPropagatesStoreError(t *testing.T) {
	store, err := settings.NewStore(sql.OpenDB(w1cErrConnector{}), true, func() time.Time { return time.UnixMilli(0) }, nil)
	if err != nil {
		t.Fatalf("构造 settings store 失败: %v", err)
	}
	reader := settingsValueReader(store)
	value, err := reader("timezone")
	if err == nil {
		t.Fatalf("store 加载失败时应向调用方透传错误")
	}
	if value != "" {
		t.Fatalf("错误路径应返回空字符串，实际 %q", value)
	}
	if !strings.Contains(err.Error(), "w1c fake 数据库不可用") {
		t.Errorf("错误应保留原始信息，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：API Key 池
// ---------------------------------------------------------------------------

func TestW1CChainAccountAPIKeyEntries(t *testing.T) {
	cases := []struct {
		name        string
		credentials map[string]any
		wantKeys    []string
		wantIndexes []int
		wantWeights []int
	}{
		{"空凭据", map[string]any{}, nil, nil, nil},
		{"单api_key去空白", map[string]any{"api_key": " key-a "}, []string{"key-a"}, []int{0}, []int{1}},
		{"api_keys优先于api_key", map[string]any{"api_keys": []any{"a", "b"}, "api_key": "c"}, []string{"a", "b"}, []int{0, 1}, []int{1, 1}},
		{"权重按原索引越界回1", map[string]any{"api_keys": []any{"a", "b", "c"}, "api_key_weights": []any{1.0, 2.0, 500.0, "x", 3.0}}, []string{"a", "b", "c"}, []int{0, 1, 2}, []int{1, 2, 1}},
		{"去重并跳过非字符串", map[string]any{"api_keys": []any{" a ", "", 42, "a", "b"}}, []string{"a", "b"}, []int{0, 4}, []int{1, 1}},
		{"空api_keys回退api_key", map[string]any{"api_keys": []any{}, "api_key": "k"}, []string{"k"}, []int{0}, []int{1}},
		{"非字符串api_key", map[string]any{"api_key": 42}, nil, nil, nil},
		{"api_keys类型非法", map[string]any{"api_keys": "a,b"}, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := chainAccountAPIKeyEntries("secret-1", tc.credentials)
			if len(entries) != len(tc.wantKeys) {
				t.Fatalf("条目数期望 %d 实际 %d", len(tc.wantKeys), len(entries))
			}
			for index := range entries {
				if entries[index].key != tc.wantKeys[index] {
					t.Errorf("第 %d 项 key 期望 %q 实际 %q", index, tc.wantKeys[index], entries[index].key)
				}
				if entries[index].index != tc.wantIndexes[index] {
					t.Errorf("第 %d 项 index 期望 %d 实际 %d", index, tc.wantIndexes[index], entries[index].index)
				}
				if entries[index].weight != tc.wantWeights[index] {
					t.Errorf("第 %d 项 weight 期望 %d 实际 %d", index, tc.wantWeights[index], entries[index].weight)
				}
				if entries[index].fingerprint == "" {
					t.Errorf("第 %d 项指纹不应为空", index)
				}
			}
		})
	}
	// 指纹是 HMAC-SHA256(secret, key) 的十六进制（独立测试向量，openssl 预算）。
	entries := chainAccountAPIKeyEntries("secret-1", map[string]any{"api_key": "key-a"})
	if len(entries) != 1 || entries[0].fingerprint != "af24b34022c1002fa95298ad639ca05ff9a7cf7917c90205899308cdab955795" {
		t.Fatalf("指纹向量不符，实际 %#v", entries)
	}
}

func TestW1CChainAccountAPIKeyPoolProviderSupported(t *testing.T) {
	cases := []struct {
		name            string
		providerCode    string
		protocolCode    string
		protocolVersion string
		want            bool
	}{
		{"openai大小写", "OpenAI", "", "", true},
		{"gpt带空白", " gpt ", "", "", true},
		{"xai", "xai", "", "", true},
		{"deepseek", "deepseek", "", "", true},
		{"glm", "glm", "", "", true},
		{"gemini", "Gemini", "", "", true},
		{"hybrid", "Hybrid", "", "", true},
		{"anthropic供应商", "Anthropic", "", "", true},
		{"未知供应商", "mystery", "", "", false},
		{"anthropic协议v1", "", "anthropic", "v1", true},
		{"anthropic协议带空白", "", " Anthropic ", " V1 ", true},
		{"anthropic协议版本不符", "", "anthropic", "v2", false},
		{"供应商不符但协议支持", "mystery", "anthropic", "v1", true},
		{"openai协议不算", "", "openai", "v1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainAccountAPIKeyPoolProviderSupported(tc.providerCode, tc.protocolCode, tc.protocolVersion); got != tc.want {
				t.Fatalf("期望 %v 实际 %v", tc.want, got)
			}
		})
	}
}

func TestW1CChainAccountAPIKeyPoolIsolationEnabled(t *testing.T) {
	cases := []struct {
		name            string
		providerCode    string
		protocolCode    string
		protocolVersion string
		accountType     string
		credentials     map[string]any
		want            bool
	}{
		{"oauth账户直接排除", "openai", "", "", "oauth", map[string]any{"api_keys": []any{"a", "b"}}, false},
		{"双键池启用", "OpenAI", "", "", "api_key", map[string]any{"api_keys": []any{"a", "b"}}, true},
		{"供应商不支持", "mystery", "", "", "api_key", map[string]any{"api_keys": []any{"a", "b"}}, false},
		{"anthropic协议支持", "", "anthropic", "v1", "api_key", map[string]any{"api_keys": []any{"a", "b"}}, true},
		{"单键不启用", "openai", "", "", "api_key", map[string]any{"api_key": "a"}, false},
		{"重复键坍缩后不启用", "openai", "", "", "api_key", map[string]any{"api_keys": []any{"a", " a "}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainAccountAPIKeyPoolIsolationEnabled(tc.providerCode, tc.protocolCode, tc.protocolVersion, tc.accountType, tc.credentials)
			if got != tc.want {
				t.Fatalf("期望 %v 实际 %v", tc.want, got)
			}
		})
	}
}

func TestW1CChainBaseURLOf(t *testing.T) {
	cases := []struct {
		name            string
		credentials     map[string]any
		protocolCode    string
		protocolVersion string
		want            string
	}{
		{"显式base_url优先", map[string]any{"base_url": "https://custom.example.com/v1"}, "", "", "https://custom.example.com/v1"},
		{"空base_url回退anthropic", map[string]any{"base_url": ""}, "anthropic", "v1", "https://api.anthropic.com/v1"},
		{"无base_url回退anthropic带空白", map[string]any{}, " ANTHROPIC ", " V1 ", "https://api.anthropic.com/v1"},
		{"gemini回退", map[string]any{}, "gemini", "v1beta", "https://generativelanguage.googleapis.com"},
		{"gemini版本不匹配回退openai", map[string]any{}, "gemini", "v1", "https://api.openai.com/v1"},
		{"默认openai", map[string]any{}, "", "", "https://api.openai.com/v1"},
		{"非字符串base_url回退openai", map[string]any{"base_url": 42}, "", "", "https://api.openai.com/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chainBaseURLOf(tc.credentials, tc.protocolCode, tc.protocolVersion); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：endpoint modes
// ---------------------------------------------------------------------------

func TestW1CChainNormalizeGatewayEndpointModesForRuntime(t *testing.T) {
	cases := []struct {
		name          string
		value         []any
		providerCode  string
		accountType   string
		compatibility string
		profileID     string
		protocolCode  string
		protocolVer   string
		want          []string
	}{
		{"hybrid过滤", []any{"chat_json", "messages_json", "bogus", "chat_json"}, "Hybrid", "", "", "", "", "", []string{"chat_json", "messages_json"}},
		{"hybrid优先于anthropic协议", []any{"chat_json", "messages_json"}, "hybrid", "", "", "", "anthropic", "v1", []string{"chat_json", "messages_json"}},
		{"anthropic默认", []any{"messages_json", "chat_json", "bogus"}, "", "", "", "", "anthropic", "v1", []string{"messages_json"}},
		{"anthropic deepseek profile", []any{"messages_json", "messages_sse", "message_token_counting"}, "", "", "", "profile_deepseek_anthropic_v1", "anthropic", "v1", []string{"messages_json", "messages_sse"}},
		{"anthropic glm coding profile", []any{"messages_json", "messages_sse", "message_token_counting"}, "", "", "", "profile_glm_coding_anthropic_v1", "anthropic", "v1", []string{"messages_json", "messages_sse"}},
		{"gemini过滤", []any{"generate_content_json", "chat_json"}, "", "", "", "", "gemini", "v1beta", []string{"generate_content_json"}},
		{"默认走openai分支", []any{"chat_json"}, "openai", "api_key", "", "", "", "", []string{"chat_json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainNormalizeGatewayEndpointModesForRuntime(tc.value, tc.providerCode, tc.accountType, tc.compatibility, tc.profileID, tc.protocolCode, tc.protocolVer)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("期望 %#v 实际 %#v", tc.want, got)
			}
		})
	}
}

func TestW1CChainNormalizeOpenAIEndpointModesForRuntime(t *testing.T) {
	allModes := []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	chatModes := []string{"chat_json", "chat_sse"}
	cases := []struct {
		name          string
		value         []any
		providerCode  string
		accountType   string
		compatibility string
		want          []string
	}{
		{"显式模式过滤去重", []any{"chat_sse", "bogus", "responses_json", "chat_sse"}, "", "api_key", "", []string{"chat_sse", "responses_json"}},
		{"oauth回落responses", nil, "", "oauth", "", []string{"responses_json", "responses_sse"}},
		{"gpt全模式", nil, "GPT", "api_key", "", allModes},
		{"deepseek全模式", []any{}, "deepseek", "api_key", "", allModes},
		{"openai聊天模式", nil, "openai", "api_key", "", chatModes},
		{"glm聊天模式", nil, " GLM ", "api_key", "", chatModes},
		{"codex兼容全模式", nil, "unknown", "api_key", "codex_responses", allModes},
		{"默认全模式", nil, "unknown", "api_key", "", allModes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainNormalizeOpenAIEndpointModesForRuntime(tc.value, tc.providerCode, tc.accountType, tc.compatibility)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("期望 %#v 实际 %#v", tc.want, got)
			}
		})
	}
}

func TestW1CChainFilterEndpointModes(t *testing.T) {
	if got := chainFilterEndpointModes(nil, []string{"chat_json"}); got != nil {
		t.Fatalf("nil 输入应保持 nil，实际 %#v", got)
	}
	empty := chainFilterEndpointModes([]any{}, []string{"chat_json"})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空数组输入应返回空切片，实际 %#v", empty)
	}
	got := chainFilterEndpointModes(
		[]any{"chat_json", 42, "chat_json", "responses_json", "bogus"},
		[]string{"chat_json", "responses_json"},
	)
	want := []string{"chat_json", "responses_json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("期望 %#v 实际 %#v", want, got)
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：资源行投影
// ---------------------------------------------------------------------------

func TestW1CChainResourceClientCompatibility(t *testing.T) {
	cases := []struct {
		name          string
		resource      string
		resourceValid bool
		rowValue      string
		rowValid      bool
		want          string
	}{
		{"资源行优先", " codex_responses ", true, "openai_standard", true, "codex_responses"},
		{"回退账户行", "", false, "openai_standard", true, "openai_standard"},
		{"两者缺失默认", "", false, "", false, "openai_standard"},
		{"资源空白回默认", "   ", true, "codex_responses", true, "openai_standard"},
		{"账户行空白回默认", "", false, "  ", true, "openai_standard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := &chainCandidateRow{
				ResourceClientCompatibility: sql.NullString{String: tc.resource, Valid: tc.resourceValid},
				ClientCompatibility:         sql.NullString{String: tc.rowValue, Valid: tc.rowValid},
			}
			if got := chainResourceClientCompatibility(row, "openai", "openai", "v1"); got != tc.want {
				t.Fatalf("期望 %q 实际 %q", tc.want, got)
			}
		})
	}
}

func TestW1CChainResolveProxyURL(t *testing.T) {
	proxyURL := "socks5h://127.0.0.1:1080"
	found := chainProxyProfileResolution{proxyURL: &proxyURL}
	unavailableMessage := "代理不存在或已停用，请选择一个已启用的代理"
	cases := []struct {
		name            string
		profileID       string
		profiles        map[string]chainProxyProfileResolution
		wantURL         string
		wantUnavailable bool
		wantMessage     string
	}{
		{"空ID直接零值", "", map[string]chainProxyProfileResolution{"p1": found}, "", false, ""},
		{"命中预取解析", "p1", map[string]chainProxyProfileResolution{"p1": found}, proxyURL, false, ""},
		{"未命中返回不可用", "p2", map[string]chainProxyProfileResolution{"p1": found}, "", true, unavailableMessage},
		{"无预取表返回不可用", "p3", nil, "", true, unavailableMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainResolveProxyURL(tc.profileID, tc.profiles)
			if tc.wantURL == "" {
				if got.proxyURL != nil {
					t.Fatalf("代理 URL 应为 nil，实际 %q", *got.proxyURL)
				}
			} else if got.proxyURL == nil || *got.proxyURL != tc.wantURL {
				t.Fatalf("代理 URL 期望 %q 实际 %#v", tc.wantURL, got.proxyURL)
			}
			if unavailable := got.unavailable != nil && *got.unavailable; unavailable != tc.wantUnavailable {
				t.Fatalf("unavailable 期望 %v 实际 %v", tc.wantUnavailable, unavailable)
			}
			if tc.wantMessage == "" {
				if got.errorMessage != nil {
					t.Fatalf("错误消息应为 nil，实际 %q", *got.errorMessage)
				}
			} else if got.errorMessage == nil || *got.errorMessage != tc.wantMessage {
				t.Fatalf("错误消息期望 %q 实际 %#v", tc.wantMessage, got.errorMessage)
			}
		})
	}
}

func TestW1CChainResourceAuthorizationQuotaLimited(t *testing.T) {
	cases := []struct {
		name   string
		limits *string
		want   bool
	}{
		{"nil输入", nil, false},
		{"空串", w1cStringPtr(""), false},
		{"空白串", w1cStringPtr("   "), false},
		{"非法JSON", w1cStringPtr("{bad"), false},
		{"空对象", w1cStringPtr("{}"), false},
		{"小时限额", w1cStringPtr(`{"hourly":{"enabled":true}}`), true},
		{"总限额", w1cStringPtr(`{"total":{"enabled":true}}`), true},
		{"全部关闭", w1cStringPtr(`{"hourly":{"enabled":false},"daily":{"enabled":false},"weekly":{"enabled":false},"monthly":{"enabled":false},"total":{"enabled":false}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chainResourceAuthorizationQuotaLimited(tc.limits)
			if got == nil {
				t.Fatalf("应返回非 nil 布尔指针")
			}
			if *got != tc.want {
				t.Fatalf("期望 %v 实际 %v", tc.want, *got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：分组类型与调度策略
// ---------------------------------------------------------------------------

func TestW1CChainNormalizeGroupType(t *testing.T) {
	cases := []struct {
		name      string
		value     sql.NullString
		want      string
		wantError string
	}{
		{"NULL回personal", sql.NullString{}, "personal", ""},
		{"空串回personal", sql.NullString{String: "", Valid: true}, "personal", ""},
		{"空白回personal", sql.NullString{String: "  ", Valid: true}, "personal", ""},
		{"personal透传", sql.NullString{String: "personal", Valid: true}, "personal", ""},
		{"high_concurrency透传", sql.NullString{String: "high_concurrency", Valid: true}, "high_concurrency", ""},
		{"非法值报错", sql.NullString{String: "shared", Valid: true}, "", "分组类型无效"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainNormalizeGroupType(tc.value)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("期望错误 %q 实际 %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错，实际 %v", err)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("期望 %q 实际 %#v", tc.want, got)
			}
		})
	}
}

func TestW1CChainParseGroupSchedulingPolicy(t *testing.T) {
	high := "high_concurrency"
	personal := "personal"
	cases := []struct {
		name     string
		value    sql.NullString
		group    *string
		wantErr  bool
		wantKeys int
	}{
		{"非高并发返回nil", sql.NullString{String: "{}", Valid: true}, &personal, false, 0},
		{"组类型nil返回nil", sql.NullString{String: "{}", Valid: true}, nil, false, 0},
		{"高并发NULL报错", sql.NullString{}, &high, true, 0},
		{"高并发空串报错", sql.NullString{String: "", Valid: true}, &high, true, 0},
		{"高并发空白报错", sql.NullString{String: "  ", Valid: true}, &high, true, 0},
		{"合法JSON", sql.NullString{String: `{"maxConcurrent":5}`, Valid: true}, &high, false, 1},
		{"非法JSON", sql.NullString{String: "{bad", Valid: true}, &high, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := chainParseGroupSchedulingPolicy(tc.value, tc.group)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应返回错误")
				}
				if policy != nil {
					t.Fatalf("错误路径策略应为 nil，实际 %#v", policy)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错，实际 %v", err)
			}
			if tc.wantKeys == 0 {
				if policy != nil {
					t.Fatalf("非高并发应返回 nil 策略，实际 %#v", policy)
				}
				return
			}
			if policy == nil || len(*policy) != tc.wantKeys {
				t.Fatalf("策略键数期望 %d 实际 %#v", tc.wantKeys, policy)
			}
			if value, ok := (*policy)["maxConcurrent"].(float64); !ok || value != 5 {
				t.Fatalf("maxConcurrent 期望 5，实际 %#v", (*policy)["maxConcurrent"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_accounts_secret.go：时间与标量 helper
// ---------------------------------------------------------------------------

func TestW1CChainOptionalRFC3339Helpers(t *testing.T) {
	valid := "2024-01-02T03:04:05.000Z"
	cases := []struct {
		name    string
		value   sql.NullString
		wantNil bool
		wantErr bool
	}{
		{"NULL输入", sql.NullString{}, true, false},
		{"空串输入", sql.NullString{String: "", Valid: true}, true, false},
		{"合法UTC", sql.NullString{String: valid, Valid: true}, false, false},
		{"合法offset", sql.NullString{String: "2024-01-02T11:04:05+08:00", Valid: true}, false, false},
		{"非法文本", sql.NullString{String: "not-a-time", Valid: true}, true, true},
		{"缺少时区", sql.NullString{String: "2024-01-02T03:04:05", Valid: true}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainOptionalRFC3339(tc.value, "过期时间")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应返回错误")
				}
				if got != nil {
					t.Fatalf("错误路径结果应为 nil，实际 %#v", got)
				}
				if !strings.Contains(err.Error(), "过期时间") || !strings.Contains(err.Error(), "必须是带 Z 或数值 offset 的 RFC3339 时间") {
					t.Errorf("错误消息应带标签与格式说明，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错，实际 %v", err)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("期望 nil，实际 %#v", got)
				}
				return
			}
			if got == nil || *got != tc.value.String {
				t.Fatalf("应透传原字符串，实际 %#v", got)
			}
		})
	}

	if got, err := chainOptionalRFC3339Raw(nil, "标签"); got != nil || err != nil {
		t.Fatalf("nil 输入应返回 (nil,nil)，实际 (%#v,%v)", got, err)
	}
	empty := ""
	if got, err := chainOptionalRFC3339Raw(&empty, "标签"); got != nil || err != nil {
		t.Fatalf("空串输入应返回 (nil,nil)，实际 (%#v,%v)", got, err)
	}
	okValue := valid
	if got, err := chainOptionalRFC3339Raw(&okValue, "标签"); err != nil || got != &okValue {
		t.Fatalf("合法值应透传原指针，实际 (%#v,%v)", got, err)
	}
	badValue := "oops"
	got, err := chainOptionalRFC3339Raw(&badValue, "创建时间")
	if err == nil || got != nil {
		t.Fatalf("非法值应返回错误且结果为 nil，实际 (%#v,%v)", got, err)
	}
	if !strings.Contains(err.Error(), "创建时间") {
		t.Errorf("错误消息应带标签，实际 %v", err)
	}
}

func TestW1CChainRFC3339Millis(t *testing.T) {
	utcMs := time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC).UnixMilli()
	noMillisMs := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()
	cases := []struct {
		name    string
		value   string
		wantMs  int64
		wantErr bool
	}{
		{"纪元零点", "1970-01-01T00:00:00Z", 0, false},
		{"毫秒UTC", "2024-01-02T03:04:05.678Z", utcMs, false},
		{"offset换算", "2024-01-02T11:04:05.678+08:00", utcMs, false},
		{"无毫秒", "2024-01-02T03:04:05Z", noMillisMs, false},
		{"首尾空白", "  2024-01-02T03:04:05Z  ", noMillisMs, false},
		{"非法文本", "not-a-time", 0, true},
		{"缺少offset", "2024-01-02T03:04:05", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainRFC3339Millis(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应返回错误，实际 %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错，实际 %v", err)
			}
			if got != tc.wantMs {
				t.Fatalf("毫秒期望 %d 实际 %d", tc.wantMs, got)
			}
		})
	}
}

func TestW1CChainStreamFailureCountConfigRevisionNullInt64(t *testing.T) {
	countCases := []struct {
		name  string
		value sql.NullInt64
		want  int
	}{
		{"NULL", sql.NullInt64{}, 0},
		{"负数", sql.NullInt64{Int64: -5, Valid: true}, 0},
		{"零", sql.NullInt64{Int64: 0, Valid: true}, 0},
		{"正数", sql.NullInt64{Int64: 7, Valid: true}, 7},
	}
	for _, tc := range countCases {
		t.Run("流失败计数_"+tc.name, func(t *testing.T) {
			if got := chainStreamFailureCount(tc.value); got != tc.want {
				t.Fatalf("期望 %d 实际 %d", tc.want, got)
			}
		})
	}

	revisionCases := []struct {
		name  string
		value sql.NullInt64
		want  int64
	}{
		{"NULL默认1", sql.NullInt64{}, 1},
		{"显式值", sql.NullInt64{Int64: 5, Valid: true}, 5},
		{"显式零", sql.NullInt64{Int64: 0, Valid: true}, 0},
	}
	for _, tc := range revisionCases {
		t.Run("配置版本_"+tc.name, func(t *testing.T) {
			got := chainConfigRevisionOf(tc.value)
			if got == nil || *got != tc.want {
				t.Fatalf("期望 %d 实际 %#v", tc.want, got)
			}
		})
	}

	ptrCases := []struct {
		name    string
		value   sql.NullInt64
		wantNil bool
		want    int64
	}{
		{"NULL", sql.NullInt64{}, true, 0},
		{"正数", sql.NullInt64{Int64: 42, Valid: true}, false, 42},
		{"负数", sql.NullInt64{Int64: -3, Valid: true}, false, -3},
	}
	for _, tc := range ptrCases {
		t.Run("可空整数_"+tc.name, func(t *testing.T) {
			got := nullInt64Ptr(tc.value)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("期望 nil，实际 %#v", got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("期望 %d 实际 %#v", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// chain_compose.go：辅助派发器接线与缺席臂
// ---------------------------------------------------------------------------

func TestW1CWireChainHybridAuxiliaryTransportAndNilCacheArms(t *testing.T) {
	dispatcher := newChainHybridAuxiliaryDispatcher(nil)
	if dispatcher == nil {
		t.Fatalf("构造器不应返回 nil")
	}
	if dispatcher.cache != nil {
		t.Fatalf("nil cache 应保持 nil")
	}
	if dispatcher.driver == nil {
		t.Fatalf("驱动不应为 nil")
	}

	// 接线只命中具体类型；fake 与 nil 安全跳过。
	pool := sharedupstreamhttp.NewClientPool()
	transport := gatewaydispatch.TransportDeps{ClientPool: pool}
	wireChainHybridAuxiliaryTransport(dispatcher, transport)
	if dispatcher.transport.ClientPool != pool {
		t.Fatalf("transport 未注入辅助派发器")
	}
	wireChainHybridAuxiliaryTransport(&w1cFakeAuxiliaryDispatcher{}, transport)
	wireChainHybridAuxiliaryTransport(nil, transport)

	// nil 接收者与 nil cache 都走派发错误臂（确定性，无网络等待）。
	input := gatewayhybrid.AuxiliaryDispatchInput{
		DispatchErrorCode:     "dispatch-code",
		DispatchErrorMessage:  "dispatch-msg",
		NoAccountErrorCode:    "no-account-code",
		NoAccountErrorMessage: "no-account-msg",
	}
	var nilDispatcher *chainHybridAuxiliaryDispatcher
	success, failure := nilDispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), input)
	if failure == nil || failure.ErrorCode != "dispatch-code" || failure.ErrorMessage != "dispatch-msg" ||
		failure.Account != nil || failure.HasGroupID || failure.HasStatusCode || failure.ShouldRecordUsage {
		t.Fatalf("nil 接收者应返回派发错误臂，实际 failure=%+v", failure)
	}
	if success.StatusCode != 0 || success.Finish != nil {
		t.Fatalf("成功臂应保持零值，实际 StatusCode=%d FinishNil=%v", success.StatusCode, success.Finish == nil)
	}

	success2, failure2 := dispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), input)
	if failure2 == nil || failure2.ErrorCode != "dispatch-code" || failure2.ErrorMessage != "dispatch-msg" ||
		failure2.Account != nil || failure2.HasGroupID || failure2.ShouldRecordUsage {
		t.Fatalf("nil cache 应返回派发错误臂，实际 failure=%+v", failure2)
	}
	if success2.Finish != nil {
		t.Fatalf("成功臂应保持零值")
	}
}
