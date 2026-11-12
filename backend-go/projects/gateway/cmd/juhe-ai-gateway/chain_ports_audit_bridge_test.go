package main

// 审计类型桥等价性测试（2026-11-03）：decodeAuditWireInput 由「JSON
// Marshal→Unmarshal 类型桥」改为逐字段直接赋值后，必须在**所有可达输入形态**
// 上产出与旧实现逐字节相同的存储侧输入。旧实现保留在本文件
// （legacyDecodeAuditWireInput）作为 oracle，测试逐例比对两边再序列化的字节。

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// legacyDecodeAuditWireInput 是改造前的实现原样副本，仅作测试 oracle。
func legacyDecodeAuditWireInput(input gatewayusage.AuditLogInput) (auditlog.AuditLogInput, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return auditlog.AuditLogInput{}, err
	}
	var decoded auditlog.AuditLogInput
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return auditlog.AuditLogInput{}, err
	}
	return decoded, nil
}

// auditBridgeFixtures 覆盖字段维度：全部标量、三处时长指针、枚举、空/多值
// header、空/非空 body、nil/空切片、attempts 全字段。
func auditBridgeFixtures(t *testing.T) map[string]gatewayusage.AuditLogInput {
	t.Helper()
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }

	full := gatewayusage.AuditLogInput{
		ID:                     "audit-full",
		LifecycleStatus:        gatewayusage.AuditLifecycleFinalized,
		TraceID:                "trace-full",
		ConversationKey:        "conv-1",
		SessionID:              "sess-1",
		SessionClientType:      "codex",
		TrafficSource:          "gateway",
		SystemAccountID:        "sys_admin",
		APIKeyID:               "key_1",
		GroupID:                "grp_1",
		RouteGroupID:           "grp_route",
		AccountID:              "acc_1",
		ProviderCode:           "gpt",
		Method:                 "POST",
		Path:                   "/v1/chat/completions",
		QueryString:            "a=b",
		Model:                  "gpt-5",
		UpstreamModel:          "gpt-5-up",
		PricingModel:           "gpt-5-price",
		ModelMappingApplied:    boolPtr(true),
		ModelMappingSource:     "explicit",
		SourceEndpointFamily:   "chat_completions",
		UpstreamEndpointFamily: "responses",
		Stream:                 boolPtr(true),
		ClientIP:               "10.0.0.1",
		UserAgent:              "ua/1.0",
		AuditOutcome:           gatewayusage.AuditOutcomeSuccess,
		Success:                true,
		FinalStatusCode:        intPtr(200),
		ErrorPhase:             "",
		ErrorCode:              "",
		ErrorMessage:           "",
		SampleBucket:           3,
		SampleReason:           "always",
		CaptureStatus:          gatewayusage.AuditCaptureComplete,
		StartedAt:              "2026-11-03T00:00:00.000Z",
		EndedAt:                "2026-11-03T00:00:01.500Z",
		DurationMs:             intPtr(1500),
		HTTPCompletedAt:        "2026-11-03T00:00:01.600Z",
		HTTPDurationMs:         intPtr(1600),
		FirstTokenMs:           intPtr(320),
		CreatedAt:              "2026-11-03T00:00:00.100Z",
		Attempts: []gatewayusage.AuditLogAttemptInput{{
			ID:                          "att-1",
			TempID:                      "attempt_0_x",
			AttemptIndex:                0,
			AccountID:                   "acc_1",
			AccountOwnerSystemAccountID: "sys_admin",
			GroupID:                     "grp_1",
			MemberGroupID:               "grp_member",
			ProxyURL:                    "socks5h://127.0.0.1:17892",
			ProviderCode:                "gpt",
			Model:                       "gpt-5",
			UpstreamModel:               "gpt-5-up",
			PricingModel:                "gpt-5-price",
			ModelMappingApplied:         boolPtr(false),
			ModelMappingSource:          "none",
			SourceEndpointFamily:        "chat_completions",
			UpstreamEndpointFamily:      "responses",
			UpstreamMethod:              "POST",
			UpstreamURL:                 "https://up.invalid/v1/responses",
			UpstreamStatusCode:          intPtr(429),
			Success:                     boolPtr(false),
			ErrorPhase:                  "dispatch",
			ErrorCode:                   "account_rpm_limit",
			ErrorMessage:                "达到每分钟上限",
			StartedAt:                   "2026-11-03T00:00:00.200Z",
			EndedAt:                     "2026-11-03T00:00:00.900Z",
			DurationMs:                  intPtr(700),
		}, {
			// 第二 attempt 只填必填，覆盖零值/缺省字段。
			AttemptIndex:   1,
			UpstreamMethod: "POST",
			UpstreamURL:    "https://up2.invalid/v1/responses",
			StartedAt:      "2026-11-03T00:00:00.950Z",
		}},
		Payloads: []gatewayusage.AuditLogPayloadInput{{
			ID:            "pay-1",
			AttemptTempID: "attempt_0_x",
			PartType:      gatewayusage.AuditPartClientRequest,
			SequenceIndex: intPtr(0),
			ContentType:   "application/json",
			// 单值 header（http.Header 的单元素切片与字面量 string 两种来源）。
			Headers: map[string]any{
				"content-type":      "application/json",
				"x-single-slice":    []string{"only"},
				"x-multi":           []string{"a", "b"},
				"x-empty-slice":     []string{},
				"x-any-strings":     []any{"p", "q"},
				"x-any-single-text": []any{"solo"},
			},
			Body:             []byte(`{"model":"gpt-5","messages":[]}`),
			HasBody:          true,
			BodySha256:       "sha-1",
			RawBodySizeBytes: intPtr(33),
			CaptureStatus:    gatewayusage.AuditCaptureComplete,
			DropReason:       "",
			CreatedAt:        "2026-11-03T00:00:00.150Z",
		}, {
			ID:              "pay-2",
			PartType:        gatewayusage.AuditPartGatewayMetadata,
			ContentEncoding: "gzip",
			// 空 header map 与空 body。
			Headers:       map[string]any{},
			Body:          nil,
			HasBody:       false,
			CaptureStatus: gatewayusage.AuditCaptureSummaryOnly,
			DropReason:    gatewayusage.AuditDropCapacityLimit,
		}, {
			// nil headers + 非空 body（大 body 单列用例在下方单独覆盖）。
			PartType: gatewayusage.AuditPartGatewayError,
			Body:     []byte(`{"error":"x"}`),
			HasBody:  true,
		}},
	}

	minimal := gatewayusage.AuditLogInput{
		// 只填 json 无 omitempty 的必填字段，其余保持零值。
		TraceID:      "trace-min",
		Method:       "GET",
		Path:         "/v1/models",
		AuditOutcome: gatewayusage.AuditOutcomeGatewaySucceeded,
		StartedAt:    "2026-11-03T01:00:00.000Z",
		EndedAt:      "2026-11-03T01:00:00.010Z",
	}

	// attempts/payloads 为 nil（旧往返：null → nil）。
	nilSlices := full
	nilSlices.ID = "audit-nil-slices"
	nilSlices.Attempts = nil
	nilSlices.Payloads = nil

	// attempts/payloads 为空切片（旧往返：[] → 空切片非 nil）。
	emptySlices := full
	emptySlices.ID = "audit-empty-slices"
	emptySlices.Attempts = []gatewayusage.AuditLogAttemptInput{}
	emptySlices.Payloads = []gatewayusage.AuditLogPayloadInput{}

	// 显式 finalized 生命周期（F3 终态唯一写入：wire 输入恒终态）。
	explicitFinalized := full
	explicitFinalized.ID = "audit-explicit-finalized"
	explicitFinalized.LifecycleStatus = gatewayusage.AuditLifecycleFinalized
	explicitFinalized.Payloads = []gatewayusage.AuditLogPayloadInput{{ID: "pay-f", PartType: gatewayusage.AuditPartClientRequest}}

	// 时长指针为 nil（omitempty 键缺失）。
	noDurations := full
	noDurations.ID = "audit-no-durations"
	noDurations.DurationMs = nil
	noDurations.HTTPDurationMs = nil
	noDurations.FirstTokenMs = nil
	noDurations.Attempts[0].DurationMs = nil

	// 二分字节 body（含 0x00/0xff）——base64 往返必须无损。
	binaryBody := full
	binaryBody.ID = "audit-binary-body"
	binaryBody.Payloads = []gatewayusage.AuditLogPayloadInput{{
		PartType: gatewayusage.AuditPartUpstreamResponse,
		Body:     []byte{0x00, 0x01, 0xff, 0xfe, 'a', 'b'},
		HasBody:  true,
	}}

	return map[string]gatewayusage.AuditLogInput{
		"full":               full,
		"minimal":            minimal,
		"nil-slices":         nilSlices,
		"empty-slices":       emptySlices,
		"explicit-finalized": explicitFinalized,
		"no-durations":       noDurations,
		"binary-body":        binaryBody,
	}
}

// TestDecodeAuditWireInputMatchesJSONBridge 是改造的核心安全网：对每个 fixture
// 分别走旧往返与新直接转换，要求两边产物再序列化后逐字节相等。
func TestDecodeAuditWireInputMatchesJSONBridge(t *testing.T) {
	for name, input := range auditBridgeFixtures(t) {
		t.Run(name, func(t *testing.T) {
			legacy, legacyErr := legacyDecodeAuditWireInput(input)
			converted, convertedErr := decodeAuditWireInput(input)

			if (legacyErr != nil) != (convertedErr != nil) {
				t.Fatalf("错误臂不一致：legacy=%v converted=%v", legacyErr, convertedErr)
			}
			if legacyErr != nil {
				return
			}

			legacyJSON, err := json.Marshal(legacy)
			if err != nil {
				t.Fatalf("marshal legacy: %v", err)
			}
			convertedJSON, err := json.Marshal(converted)
			if err != nil {
				t.Fatalf("marshal converted: %v", err)
			}
			if !bytes.Equal(legacyJSON, convertedJSON) {
				t.Fatalf("新实现产物与旧 wire 往返不等价\n legacy=%s\n new   =%s", legacyJSON, convertedJSON)
			}
		})
	}
}

// TestDecodeAuditWireInputBigBodyLossless：大 body 过桥必须字节无损（旧路径
// 的 base64 往返等价性在新实现中由切片拷贝保证）。
func TestDecodeAuditWireInputBigBodyLossless(t *testing.T) {
	bigBody := bytes.Repeat([]byte("0123456789abcdef"), 200*1024) // ~3.2MB
	input := gatewayusage.AuditLogInput{
		TraceID:      "trace-big",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		AuditOutcome: gatewayusage.AuditOutcomeSuccess,
		StartedAt:    "2026-11-03T02:00:00.000Z",
		EndedAt:      "2026-11-03T02:00:01.000Z",
		Payloads: []gatewayusage.AuditLogPayloadInput{{
			PartType: gatewayusage.AuditPartClientRequest,
			Body:     bigBody,
			HasBody:  true,
		}},
	}
	converted, err := decodeAuditWireInput(input)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(converted.Payloads) != 1 {
		t.Fatalf("payloads=%d", len(converted.Payloads))
	}
	body := converted.Payloads[0].Body
	if !body.Present || !bytes.Equal(body.Bytes, bigBody) {
		t.Fatalf("大 body 无损失败：present=%v len=%d want=%d", body.Present, len(body.Bytes), len(bigBody))
	}
	// 解码侧必须持有独立字节（旧 base64 往返产生新切片）。
	if len(body.Bytes) > 0 && &body.Bytes[0] == &bigBody[0] {
		t.Fatal("解码产物不得与采集侧共享底层数组")
	}
}

// TestAuditHeadersFromWireRejectsNonString 固定失败语义：非字符串 header 与
// 旧往返一样返回错误（整条采集丢弃），不静默降级。
func TestAuditHeadersFromWireRejectsNonString(t *testing.T) {
	cases := map[string]map[string]any{
		"int":       {"x-num": 42},
		"bool":      {"x-bool": true},
		"float":     {"x-float": 1.5},
		"obj":       {"x-obj": map[string]any{"a": 1}},
		"any-int":   {"x-mixed": []any{"ok", 1}},
		"any-array": {"x-nested": []any{[]string{"a"}}},
		"typed-2d":  {"x-typed": [][]string{{"a"}}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := auditHeadersFromWire(headers); err == nil {
				t.Fatalf("必须拒绝非字符串 header：%v", headers)
			}
		})
	}
}

// TestAuditHeadersFromWireExhaustiveShapes 穷举 header 值形态并与旧 wire 往返
// 逐结构比对。三种「nil 降级为空串」的形态（裸 nil、[]string(nil)、
// []any{nil}）在 2026-11-03 复查中发现过实现缺口，这里固定住：旧路径不会
// 丢弃整条采集，而是把该 header 记成空串（或空串元素）。
func TestAuditHeadersFromWireExhaustiveShapes(t *testing.T) {
	shapes := map[string]any{
		"string":               "v",
		"empty-string":         "",
		"numeric-string":       "123",
		"string-slice-1":       []string{"a"},
		"string-slice-multi":   []string{"a", "b"},
		"string-slice-empty":   []string{},
		"string-slice-nil":     []string(nil),
		"any-slice-1":          []any{"a"},
		"any-slice-empty":      []any{},
		"any-slice-nil":        []any(nil),
		"any-slice-null-el":    []any{nil},
		"any-slice-mixed-null": []any{"a", nil, "b"},
		"bare-nil":             nil,
	}
	for name, value := range shapes {
		t.Run(name, func(t *testing.T) {
			headers := map[string]any{"x": value}

			// oracle：旧 wire 往返。
			encoded, err := json.Marshal(map[string]any{"headers": headers})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var legacy struct {
				Headers map[string]auditlog.HeaderValues `json:"headers"`
			}
			if err := json.Unmarshal(encoded, &legacy); err != nil {
				t.Fatalf("legacy unmarshal: %v", err)
			}

			got, err := auditHeadersFromWire(headers)
			if err != nil {
				t.Fatalf("新实现不应报错（旧路径可解码）: %v", err)
			}
			// 再序列化比对：HeaderValues 的 MarshalJSON 编码了 Array/Values 语义。
			legacyJSON, _ := json.Marshal(legacy.Headers["x"])
			gotJSON, _ := json.Marshal(got["x"])
			if !bytes.Equal(legacyJSON, gotJSON) {
				t.Fatalf("形态 %s 不等价：legacy=%s new=%s（legacy=%+v new=%+v）",
					name, legacyJSON, gotJSON, legacy.Headers["x"], got["x"])
			}
			// 结构比对（防止序列化恰好相同但内部表示不同）。
			if !headerValuesEqual(legacy.Headers["x"], got["x"]) {
				t.Fatalf("形态 %s 结构不等价：legacy=%+v new=%+v", name, legacy.Headers["x"], got["x"])
			}
		})
	}
}

func headerValuesEqual(left, right auditlog.HeaderValues) bool {
	if left.Array != right.Array || len(left.Values) != len(right.Values) {
		return false
	}
	for index := range left.Values {
		if left.Values[index] != right.Values[index] {
			return false
		}
	}
	return true
}

// TestDecodeAuditWireInputFieldSweep 逐字段扫一遍：对 full fixture 的每个
// 文本/指针字段单独置空，确认新实现与旧往返在每个字段上仍然等价——用来
// 发现「某个字段忘了搬」的漏项（漏搬会表现为该字段在 converted 里恒零值，
// 而 legacy 里保留原值，字节比对立刻失衡）。
func TestDecodeAuditWireInputFieldSweep(t *testing.T) {
	base := auditBridgeFixtures(t)["full"]
	// 每个 mutator 破坏一个字段；失败即说明该字段未被正确搬迁。
	mutators := map[string]func(*gatewayusage.AuditLogInput){
		"id":                     func(i *gatewayusage.AuditLogInput) { i.ID = "audit-sweep" },
		"lifecycle":              func(i *gatewayusage.AuditLogInput) { i.LifecycleStatus = gatewayusage.AuditLifecycleFinalized },
		"conversationKey":        func(i *gatewayusage.AuditLogInput) { i.ConversationKey = "conv-sweep" },
		"sessionId":              func(i *gatewayusage.AuditLogInput) { i.SessionID = "sess-sweep" },
		"sessionClientType":      func(i *gatewayusage.AuditLogInput) { i.SessionClientType = "client-sweep" },
		"trafficSource":          func(i *gatewayusage.AuditLogInput) { i.TrafficSource = "probe" },
		"systemAccountId":        func(i *gatewayusage.AuditLogInput) { i.SystemAccountID = "sys-sweep" },
		"apiKeyId":               func(i *gatewayusage.AuditLogInput) { i.APIKeyID = "key-sweep" },
		"groupId":                func(i *gatewayusage.AuditLogInput) { i.GroupID = "grp-sweep" },
		"routeGroupId":           func(i *gatewayusage.AuditLogInput) { i.RouteGroupID = "rgrp-sweep" },
		"accountId":              func(i *gatewayusage.AuditLogInput) { i.AccountID = "acc-sweep" },
		"providerCode":           func(i *gatewayusage.AuditLogInput) { i.ProviderCode = "xai" },
		"method":                 func(i *gatewayusage.AuditLogInput) { i.Method = "PUT" },
		"path":                   func(i *gatewayusage.AuditLogInput) { i.Path = "/v1/sweep" },
		"queryString":            func(i *gatewayusage.AuditLogInput) { i.QueryString = "sweep=1" },
		"model":                  func(i *gatewayusage.AuditLogInput) { i.Model = "m-sweep" },
		"upstreamModel":          func(i *gatewayusage.AuditLogInput) { i.UpstreamModel = "um-sweep" },
		"pricingModel":           func(i *gatewayusage.AuditLogInput) { i.PricingModel = "pm-sweep" },
		"modelMappingSource":     func(i *gatewayusage.AuditLogInput) { i.ModelMappingSource = "sweep-src" },
		"sourceEndpointFamily":   func(i *gatewayusage.AuditLogInput) { i.SourceEndpointFamily = "se-sweep" },
		"upstreamEndpointFamily": func(i *gatewayusage.AuditLogInput) { i.UpstreamEndpointFamily = "ue-sweep" },
		"clientIp":               func(i *gatewayusage.AuditLogInput) { i.ClientIP = "10.9.9.9" },
		"userAgent":              func(i *gatewayusage.AuditLogInput) { i.UserAgent = "ua-sweep" },
		"auditOutcome":           func(i *gatewayusage.AuditLogInput) { i.AuditOutcome = gatewayusage.AuditOutcomeStreamFailed },
		"success":                func(i *gatewayusage.AuditLogInput) { i.Success = false },
		"errorPhase":             func(i *gatewayusage.AuditLogInput) { i.ErrorPhase = "dispatch" },
		"errorCode":              func(i *gatewayusage.AuditLogInput) { i.ErrorCode = "sweep_code" },
		"errorMessage":           func(i *gatewayusage.AuditLogInput) { i.ErrorMessage = "sweep message" },
		"sampleBucket":           func(i *gatewayusage.AuditLogInput) { i.SampleBucket = 9 },
		"sampleReason":           func(i *gatewayusage.AuditLogInput) { i.SampleReason = "sweep-reason" },
		"captureStatus":          func(i *gatewayusage.AuditLogInput) { i.CaptureStatus = gatewayusage.AuditCaptureHashOnly },
		"startedAt":              func(i *gatewayusage.AuditLogInput) { i.StartedAt = "2026-11-03T09:00:00.000Z" },
		"endedAt":                func(i *gatewayusage.AuditLogInput) { i.EndedAt = "2026-11-03T09:00:02.000Z" },
		"httpCompletedAt":        func(i *gatewayusage.AuditLogInput) { i.HTTPCompletedAt = "2026-11-03T09:00:02.500Z" },
		"createdAt":              func(i *gatewayusage.AuditLogInput) { i.CreatedAt = "2026-11-03T09:00:00.500Z" },
		"attempt.id":             func(i *gatewayusage.AuditLogInput) { i.Attempts[0].ID = "att-sweep" },
		"attempt.tempId":         func(i *gatewayusage.AuditLogInput) { i.Attempts[0].TempID = "tmp-sweep" },
		"attempt.upstreamMethod": func(i *gatewayusage.AuditLogInput) { i.Attempts[0].UpstreamMethod = "GET" },
		"attempt.upstreamUrl":    func(i *gatewayusage.AuditLogInput) { i.Attempts[0].UpstreamURL = "https://sweep.invalid/x" },
		"attempt.groupId":        func(i *gatewayusage.AuditLogInput) { i.Attempts[0].GroupID = "grp-att-sweep" },
		"attempt.proxyUrl":       func(i *gatewayusage.AuditLogInput) { i.Attempts[0].ProxyURL = "http://p-sweep:1" },
		"attempt.errorMessage":   func(i *gatewayusage.AuditLogInput) { i.Attempts[0].ErrorMessage = "att-sweep-msg" },
		"payload.id":             func(i *gatewayusage.AuditLogInput) { i.Payloads[0].ID = "pay-sweep" },
		"payload.attemptTempId":  func(i *gatewayusage.AuditLogInput) { i.Payloads[0].AttemptTempID = "att-tmp-sweep" },
		"payload.contentType":    func(i *gatewayusage.AuditLogInput) { i.Payloads[0].ContentType = "text/plain" },
		"payload.bodySha256":     func(i *gatewayusage.AuditLogInput) { i.Payloads[0].BodySha256 = "sha-sweep" },
		"payload.createdAt":      func(i *gatewayusage.AuditLogInput) { i.Payloads[0].CreatedAt = "2026-11-03T09:00:00.700Z" },
	}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			input := base
			// 深拷贝切片，避免 mutator 改到其他子用例的共享底层数组。
			input.Attempts = append([]gatewayusage.AuditLogAttemptInput(nil), base.Attempts...)
			input.Payloads = append([]gatewayusage.AuditLogPayloadInput(nil), base.Payloads...)
			input.Payloads[0].Headers = map[string]any{"content-type": "application/json"}
			mutate(&input)

			legacy, err := legacyDecodeAuditWireInput(input)
			if err != nil {
				t.Fatalf("legacy: %v", err)
			}
			converted, err := decodeAuditWireInput(input)
			if err != nil {
				t.Fatalf("converted: %v", err)
			}
			legacyJSON, _ := json.Marshal(legacy)
			convertedJSON, _ := json.Marshal(converted)
			if !bytes.Equal(legacyJSON, convertedJSON) {
				t.Fatalf("字段 %s 未正确搬迁\n legacy=%s\n new   =%s", name, legacyJSON, convertedJSON)
			}
		})
	}
}

// BenchmarkAuditBridgeLegacyVsDirect 量化改造收益：同一输入下旧 JSON 往返
// 与新直接转换的耗时/分配对比（含 64KB 典型 body）。
func BenchmarkAuditBridgeLegacyVsDirect(b *testing.B) {
	body := bytes.Repeat([]byte(`{"role":"user","content":"hello world"}`), 2000) // ~64KB
	intPtr := func(v int) *int { return &v }
	input := gatewayusage.AuditLogInput{
		ID:           "audit-bench",
		TraceID:      "trace-bench",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		AuditOutcome: gatewayusage.AuditOutcomeSuccess,
		StartedAt:    "2026-11-03T00:00:00.000Z",
		EndedAt:      "2026-11-03T00:00:01.000Z",
		DurationMs:   intPtr(1000),
		Attempts: []gatewayusage.AuditLogAttemptInput{{
			AttemptIndex:   0,
			UpstreamMethod: "POST",
			UpstreamURL:    "https://up.invalid/v1/chat/completions",
			StartedAt:      "2026-11-03T00:00:00.100Z",
			DurationMs:     intPtr(800),
		}},
		Payloads: []gatewayusage.AuditLogPayloadInput{{
			ID:       "pay-bench-req",
			PartType: gatewayusage.AuditPartClientRequest,
			Headers:  map[string]any{"content-type": "application/json", "x-multi": []string{"a", "b"}},
			Body:     body,
			HasBody:  true,
		}, {
			ID:       "pay-bench-resp",
			PartType: gatewayusage.AuditPartUpstreamResponse,
			Headers:  map[string]any{"content-type": "text/event-stream"},
			Body:     body,
			HasBody:  true,
		}},
	}

	b.Run("legacy-json-bridge", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := legacyDecodeAuditWireInput(input); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("direct-convert", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := decodeAuditWireInput(input); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// TestDecodeAuditWireInputRandomized 随机组合字段做等价性模糊测试：手工
// fixture 会漏掉「某两个字段同时非零」这类交互，这里用确定性伪随机生成
// 上千条输入，逐条比对旧往返与新转换的再序列化字节。
func TestDecodeAuditWireInputRandomized(t *testing.T) {
	// 固定种子：可复现，不引入 flaky。
	seed := uint64(0x5eed1e55)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	pick := func(values ...string) string { return values[next()%uint64(len(values))] }
	intPtr := func(v int) *int { return &v }
	boolPtr := func(v bool) *bool { return &v }
	maybeInt := func() *int {
		if next()%3 == 0 {
			return nil
		}
		return intPtr(int(next() % 5000))
	}
	maybeID := func(prefix string) string {
		if next()%4 == 0 {
			return ""
		}
		return prefix + "-" + pick("a", "b", "c")
	}
	headerSamples := []map[string]any{
		nil,
		{},
		{"content-type": "application/json"},
		{"x-multi": []string{"a", "b"}},
		{"x-empty": []string{}},
		{"x-nil": []string(nil)},
		{"x-any": []any{"p", "q"}},
		{"x-null-el": []any{nil}},
		{"mixed": "s", "arr": []string{"1"}, "empty": []string{}},
	}

	for iteration := 0; iteration < 2000; iteration++ {
		attemptCount := int(next() % 4)
		payloadCount := int(next() % 4)
		input := gatewayusage.AuditLogInput{
			ID:                maybeID("audit"),
			LifecycleStatus:   pick("finalized", ""),
			TraceID:           "trace-" + pick("x", "y"),
			ConversationKey:   maybeID("conv"),
			SessionID:         maybeID("sess"),
			SessionClientType: pick("", "codex", "claude"),
			TrafficSource:     pick("gateway", "probe", ""),
			SystemAccountID:   maybeID("sys"),
			APIKeyID:          maybeID("key"),
			GroupID:           maybeID("grp"),
			RouteGroupID:      maybeID("rgrp"),
			AccountID:         maybeID("acc"),
			ProviderCode:      pick("", "gpt", "xai"),
			Method:            pick("POST", "GET"),
			Path:              pick("/v1/chat/completions", "/v1/responses"),
			QueryString:       pick("", "a=1"),
			Model:             pick("", "gpt-5"),
			UpstreamModel:     pick("", "gpt-5-up"),
			PricingModel:      pick("", "price-1"),
			ModelMappingApplied: func() *bool {
				if next()%2 == 0 {
					return nil
				}
				return boolPtr(next()%2 == 0)
			}(),
			ModelMappingSource:     pick("", "explicit"),
			SourceEndpointFamily:   pick("", "chat_completions"),
			UpstreamEndpointFamily: pick("", "responses"),
			Stream: func() *bool {
				if next()%2 == 0 {
					return nil
				}
				return boolPtr(true)
			}(),
			ClientIP:        pick("", "10.0.0.1"),
			UserAgent:       pick("", "ua"),
			AuditOutcome:    pick("success", "gateway_failed", ""),
			Success:         next()%2 == 0,
			FinalStatusCode: maybeInt(),
			ErrorPhase:      pick("", "dispatch"),
			ErrorCode:       pick("", "account_rpm_limit"),
			ErrorMessage:    pick("", "boom"),
			SampleBucket:    int(next() % 10),
			SampleReason:    pick("", "always"),
			CaptureStatus:   pick("", "complete", "summary_only"),
			StartedAt:       "2026-11-03T00:00:00.000Z",
			EndedAt:         "2026-11-03T00:00:01.000Z",
			DurationMs:      maybeInt(),
			HTTPCompletedAt: pick("", "2026-11-03T00:00:02.000Z"),
			HTTPDurationMs:  maybeInt(),
			FirstTokenMs:    maybeInt(),
			CreatedAt:       pick("", "2026-11-03T00:00:00.500Z"),
		}
		for index := 0; index < attemptCount; index++ {
			input.Attempts = append(input.Attempts, gatewayusage.AuditLogAttemptInput{
				ID:             maybeID("att"),
				AttemptIndex:   index,
				AccountID:      maybeID("acc"),
				UpstreamMethod: pick("POST", "GET"),
				UpstreamURL:    pick("https://u.invalid/a", "https://u.invalid/b"),
				ErrorCode:      pick("", "x"),
				StartedAt:      "2026-11-03T00:00:00.100Z",
				DurationMs:     maybeInt(),
			})
		}
		for index := 0; index < payloadCount; index++ {
			var body []byte
			switch next() % 4 {
			case 0:
				body = nil
			case 1:
				body = []byte{}
			case 2:
				body = []byte(`{"k":"v"}`)
			default:
				body = []byte{0x00, 0xff, 0x10}
			}
			input.Payloads = append(input.Payloads, gatewayusage.AuditLogPayloadInput{
				ID:            maybeID("pay"),
				PartType:      pick("client_request", "upstream_response", "gateway_error"),
				Headers:       headerSamples[int(next()%uint64(len(headerSamples)))],
				Body:          body,
				HasBody:       len(body) > 0,
				CaptureStatus: pick("", "complete"),
				DropReason:    pick("", "capacity_limit"),
			})
		}

		legacy, legacyErr := legacyDecodeAuditWireInput(input)
		converted, convertedErr := decodeAuditWireInput(input)
		if (legacyErr != nil) != (convertedErr != nil) {
			t.Fatalf("iteration %d 错误臂不一致：legacy=%v new=%v", iteration, legacyErr, convertedErr)
		}
		if legacyErr != nil {
			continue
		}
		legacyJSON, err := json.Marshal(legacy)
		if err != nil {
			t.Fatalf("iteration %d marshal legacy: %v", iteration, err)
		}
		convertedJSON, err := json.Marshal(converted)
		if err != nil {
			t.Fatalf("iteration %d marshal new: %v", iteration, err)
		}
		if !bytes.Equal(legacyJSON, convertedJSON) {
			t.Fatalf("iteration %d 不等价\n input=%+v\n legacy=%s\n new   =%s",
				iteration, input, legacyJSON, convertedJSON)
		}
	}
}
