package gatewaybody

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// 固定向量（设计 §9.7）：字面量为按设计 §4.2 公式独立计算的期望值，用于钉死
// 哈希公式不被实现侧意外改写。向量 A 覆盖多条 system 0x00 分隔、tool_calls
// 原样字节（A1）、U2 选取（中间隔 assistant/tool）；向量 B 为无 system 的最
// 小形态。
const (
	contentFingerprintVectorSystemA = `{"role":"system","content":"You are helpful."}`
	contentFingerprintVectorDevA    = `{"role":"developer","content":"Be terse."}`
	contentFingerprintVectorUserA1  = `{"role":"user","content":"Hello"}`
	contentFingerprintVectorAsstA1  = `{"role":"assistant","content":"Hi there","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_answer","arguments":"{}"}}]}`
	contentFingerprintVectorToolA   = `{"role":"tool","tool_call_id":"call_1","content":"42"}`
	contentFingerprintVectorUserA2  = `{"role":"user","content":"Second question"}`
	contentFingerprintVectorUserB1  = `{"role":"user","content":"U1"}`

	contentFingerprintVectorSysAccountA = "sys-1"
	contentFingerprintVectorAPIKeyA     = "key-1"
	contentFingerprintVectorProfileA    = "generic_openai"

	contentFingerprintVectorProvisionalA = "8ad5f7c6ef9785bc236da41b4a036724fd44596286f7fdf5fd761b1ec3a8b762"
	contentFingerprintVectorConfirmedA   = "6724de1a1e07258e79b58ecbb578991a1ec37eaeb2a9d0c4ec50171cfc2434b7"

	contentFingerprintVectorSysAccountB = "sys-2"
	contentFingerprintVectorAPIKeyB     = "key-2"
	contentFingerprintVectorProfileB    = "generic_openai"

	contentFingerprintVectorProvisionalB = "6c948e295030710b1d46bffebda7f35f49c33bfdd3153861ab11d769484a463a"
)

// testHashSegments 按设计 §4.2 独立组装 hashS（各元素 0x00 分隔拼接后 sha256）。
func testHashSegments(elements ...string) []byte {
	h := sha256.New()
	for i, element := range elements {
		if i > 0 {
			h.Write([]byte{0x00})
		}
		h.Write([]byte(element))
	}
	return h.Sum(nil)
}

// testHashElement 按设计 §4.2 独立组装单锚点元素哈希。
func testHashElement(element string) []byte {
	sum := sha256.Sum256([]byte(element))
	return sum[:]
}

// testFingerprint 按设计 §4.2 独立组装指纹公式。
func testFingerprint(domain, systemAccountID, apiKeyID, clientProfile string, anchorHashes ...[]byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	for _, part := range []string{systemAccountID, apiKeyID, clientProfile} {
		h.Write([]byte{0x00})
		h.Write([]byte(part))
	}
	for _, hash := range anchorHashes {
		h.Write([]byte{0x00})
		h.Write(hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contentFingerprintVectorBodyA() string {
	return `{"model":"gpt-x","messages":[` +
		contentFingerprintVectorSystemA + `,` +
		contentFingerprintVectorDevA + `,` +
		contentFingerprintVectorUserA1 + `,` +
		contentFingerprintVectorAsstA1 + `,` +
		contentFingerprintVectorToolA + `,` +
		contentFingerprintVectorUserA2 + `],"stream":true}`
}

func TestScanConversationAnchors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		// want 期望锚点（元素原始字节字面量）；wantMessageCount 期望 messages
		// 数组元素总数；wantConfirmed 期望确认档可计算。
		wantSystem       []string
		wantUser1        string
		wantAssistant    string
		wantUser2        string
		wantMessageCount int
		wantConfirmed    bool
	}{
		{
			name: "vector A: multi system, tool_calls A1, U2 after assistant+tool",
			raw:  contentFingerprintVectorBodyA(),
			wantSystem: []string{
				contentFingerprintVectorSystemA,
				contentFingerprintVectorDevA,
			},
			wantUser1:        contentFingerprintVectorUserA1,
			wantAssistant:    contentFingerprintVectorAsstA1,
			wantUser2:        contentFingerprintVectorUserA2,
			wantMessageCount: 6,
			wantConfirmed:    true,
		},
		{
			name:             "first request: no assistant, no U2",
			raw:              `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
			wantSystem:       []string{`{"role":"system","content":"s"}`},
			wantUser1:        `{"role":"user","content":"hi"}`,
			wantMessageCount: 2,
			wantConfirmed:    false,
		},
		{
			name:             "no system prefix: empty S segment",
			raw:              `{"messages":[{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`,
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantAssistant:    `{"role":"assistant","content":"a1"}`,
			wantUser2:        `{"role":"user","content":"u2"}`,
			wantMessageCount: 3,
			wantConfirmed:    true,
		},
		{
			name:             "system after user is not part of S",
			raw:              `{"messages":[{"role":"user","content":"u1"},{"role":"system","content":"late"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`,
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantAssistant:    `{"role":"assistant","content":"a1"}`,
			wantUser2:        `{"role":"user","content":"u2"}`,
			wantMessageCount: 4,
			wantConfirmed:    true,
		},
		{
			name:             "nested content array element",
			raw:              `{"messages":[{"role":"user","content":[{"type":"text","text":"see this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"assistant","content":"a"},{"role":"user","content":"next"}]}`,
			wantUser1:        `{"role":"user","content":[{"type":"text","text":"see this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`,
			wantAssistant:    `{"role":"assistant","content":"a"}`,
			wantUser2:        `{"role":"user","content":"next"}`,
			wantMessageCount: 3,
			wantConfirmed:    true,
		},
		{
			name:             "assistant before first user",
			raw:              `{"messages":[{"role":"assistant","content":"canned"},{"role":"user","content":"u1"}]}`,
			wantAssistant:    `{"role":"assistant","content":"canned"}`,
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantMessageCount: 2,
			wantConfirmed:    false,
		},
		{
			name:             "two users without assistant: provisional only",
			raw:              `{"messages":[{"role":"user","content":"u1"},{"role":"user","content":"u2"}]}`,
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantUser2:        `{"role":"user","content":"u2"}`,
			wantMessageCount: 2,
			wantConfirmed:    false,
		},
		{
			name:             "pretty-printed body spans exclude surrounding whitespace",
			raw:              "{\n  \"messages\": [\n    {\"role\":\"user\",\"content\":\"u1\"},\n    {\"role\":\"assistant\",\"content\":\"a1\"}\n  ]\n}",
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantAssistant:    `{"role":"assistant","content":"a1"}`,
			wantMessageCount: 2,
			wantConfirmed:    false,
		},
		{
			name:             "messages located after other top-level keys",
			raw:              `{"model":"m","stream":false,"tools":[{"type":"function"}],"messages":[{"role":"user","content":"u1"}],"max_tokens":16}`,
			wantUser1:        `{"role":"user","content":"u1"}`,
			wantMessageCount: 1,
			wantConfirmed:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchors, ok := ScanConversationAnchors([]byte(tt.raw))
			if !ok {
				t.Fatalf("ScanConversationAnchors() = no anchors, want anchors")
			}
			if got := len(anchors.SystemElements); got != len(tt.wantSystem) {
				t.Fatalf("system elements = %d, want %d", got, len(tt.wantSystem))
			}
			for i, want := range tt.wantSystem {
				if string(anchors.SystemElements[i]) != want {
					t.Errorf("system[%d] = %s, want %s", i, anchors.SystemElements[i], want)
				}
			}
			if string(anchors.FirstUser) != tt.wantUser1 {
				t.Errorf("firstUser = %s, want %s", anchors.FirstUser, tt.wantUser1)
			}
			if tt.wantAssistant != "" && string(anchors.FirstAssistant) != tt.wantAssistant {
				t.Errorf("firstAssistant = %s, want %s", anchors.FirstAssistant, tt.wantAssistant)
			}
			if tt.wantAssistant == "" && anchors.FirstAssistant != nil {
				t.Errorf("firstAssistant = %s, want nil", anchors.FirstAssistant)
			}
			if tt.wantUser2 != "" && string(anchors.SecondUser) != tt.wantUser2 {
				t.Errorf("secondUser = %s, want %s", anchors.SecondUser, tt.wantUser2)
			}
			if tt.wantUser2 == "" && anchors.SecondUser != nil {
				t.Errorf("secondUser = %s, want nil", anchors.SecondUser)
			}
			if anchors.MessageCount != tt.wantMessageCount {
				t.Errorf("messageCount = %d, want %d", anchors.MessageCount, tt.wantMessageCount)
			}
			if anchors.ConfirmedComputable() != tt.wantConfirmed {
				t.Errorf("confirmedComputable = %v, want %v", anchors.ConfirmedComputable(), tt.wantConfirmed)
			}
			if !anchors.ProvisionalComputable() {
				t.Errorf("provisionalComputable = false, want true")
			}
		})
	}
}

func TestScanConversationAnchorsFailureSemantics(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "body is a JSON array", raw: `[{"role":"user","content":"u1"}]`},
		{name: "body is a JSON string", raw: `"messages"`},
		{name: "body is a scalar", raw: `42`},
		{name: "empty body", raw: ``},
		{name: "no messages key", raw: `{"model":"m","stream":true}`},
		{name: "messages is not an array", raw: `{"messages":{"role":"user"}}`},
		{name: "messages is null", raw: `{"messages":null}`},
		{name: "element missing role", raw: `{"messages":[{"content":"no role"},{"role":"user","content":"u1"}]}`},
		{name: "element role is not a string", raw: `{"messages":[{"role":7}]}`},
		{name: "truncated inside messages array", raw: `{"messages":[{"role":"user","content":"u1"},{"role":"assi`},
		{name: "truncated before messages", raw: `{"model":"m","mes`},
		{name: "messages array never closes", raw: `{"messages":[{"role":"user","content":"u1"},`},
		{name: "empty messages array", raw: `{"messages":[]}`},
		{name: "no user message", raw: `{"messages":[{"role":"system","content":"s"},{"role":"assistant","content":"a"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchors, ok := ScanConversationAnchors([]byte(tt.raw))
			if ok {
				t.Fatalf("ScanConversationAnchors() = %+v, want no anchors", anchors)
			}
		})
	}
}

// TestScanConversationAnchorsBoundedTail 钉死成本界（设计 §4.1）：四锚齐备后
// 数组尾部只跳读计数，不读取元素内容——尾部元素缺 role 不判失败、改写尾部
// 内容不影响指纹、数组闭合后的 body 其余部分不被读取。
func TestScanConversationAnchorsBoundedTail(t *testing.T) {
	head := `{"messages":[` +
		`{"role":"system","content":"s"},` +
		`{"role":"user","content":"u1"},` +
		`{"role":"assistant","content":"a1"},` +
		`{"role":"user","content":"u2"}`

	// 尾部 50 个元素，缺 role 且 role 为非字符串的探针元素混入其中：若扫描器
	// 在四锚齐备后仍检查内容，这些元素会让扫描失败。
	var tail strings.Builder
	tail.WriteByte(',')
	for i := 0; i < 50; i++ {
		if i == 25 {
			tail.WriteString(`{"role":12345},`)
			continue
		}
		tail.WriteString(`{"content":"tail filler without role"},`)
	}
	tail.WriteString(`{"content":"last"}]`)
	withTail := head + tail.String()

	anchors, ok := ScanConversationAnchors([]byte(withTail))
	if !ok {
		t.Fatalf("ScanConversationAnchors() = no anchors, want anchors (tail must not be content-checked)")
	}
	if anchors.MessageCount != 4+51 {
		t.Errorf("messageCount = %d, want %d", anchors.MessageCount, 4+51)
	}
	if string(anchors.FirstUser) != `{"role":"user","content":"u1"}` {
		t.Errorf("firstUser = %s", anchors.FirstUser)
	}

	// 改写尾部内容只改变计数，不影响任何锚点字节与指纹。
	anchorsOtherTail, ok := ScanConversationAnchors([]byte(head + `,` + strings.Repeat(`{"content":"x"},`, 20) + `{"content":"y"}]`))
	if !ok {
		t.Fatalf("ScanConversationAnchors(other tail) = no anchors, want anchors")
	}
	if anchorsOtherTail.MessageCount != 4+21 {
		t.Errorf("other messageCount = %d, want %d", anchorsOtherTail.MessageCount, 4+21)
	}
	if string(anchorsOtherTail.FirstUser) != string(anchors.FirstUser) ||
		string(anchorsOtherTail.FirstAssistant) != string(anchors.FirstAssistant) ||
		string(anchorsOtherTail.SecondUser) != string(anchors.SecondUser) {
		t.Error("tail rewrite changed anchor bytes")
	}
	if ProvisionalFingerprintHex("sys", "key", "p", anchorsOtherTail) != ProvisionalFingerprintHex("sys", "key", "p", anchors) {
		t.Error("tail rewrite changed provisional fingerprint")
	}
	if ConfirmedFingerprintHex("sys", "key", "p", anchorsOtherTail) != ConfirmedFingerprintHex("sys", "key", "p", anchors) {
		t.Error("tail rewrite changed confirmed fingerprint")
	}

	// 数组闭合后的 body 其余部分不被读取：截断的后续键不影响锚点。
	truncatedAfterArray := head + `,{"role":"user","content":"u3"}],"trailing_key`
	anchorsTruncated, ok := ScanConversationAnchors([]byte(truncatedAfterArray))
	if !ok {
		t.Fatalf("ScanConversationAnchors(truncated after array) = no anchors, want anchors")
	}
	if anchorsTruncated.MessageCount != 5 {
		t.Errorf("truncated messageCount = %d, want 5", anchorsTruncated.MessageCount)
	}
}

// TestScanConversationAnchorsZeroCopy 断言锚点字节直接指向输入 raw 的原字。
func TestScanConversationAnchorsZeroCopy(t *testing.T) {
	raw := []byte(contentFingerprintVectorBodyA())
	anchors, ok := ScanConversationAnchors(raw)
	if !ok {
		t.Fatal("ScanConversationAnchors() = no anchors, want anchors")
	}
	for _, anchor := range [][]byte{anchors.FirstUser, anchors.FirstAssistant, anchors.SecondUser} {
		if len(anchor) == 0 {
			t.Fatal("anchor is empty")
		}
		if !bytes.Contains(raw, anchor) {
			t.Fatal("anchor bytes not aliasing raw")
		}
	}
	// 原字验证：改写 raw 中 U1 的一个字节，锚点视图随之变化。
	index := bytes.Index(raw, anchors.FirstUser)
	raw[index] = 'X'
	if anchors.FirstUser[0] != 'X' {
		t.Fatal("anchor does not share backing array with raw")
	}
}

func TestContentFingerprintFixedVectors(t *testing.T) {
	anchorsA := ConversationAnchors{
		SystemElements: [][]byte{
			[]byte(contentFingerprintVectorSystemA),
			[]byte(contentFingerprintVectorDevA),
		},
		FirstUser:      []byte(contentFingerprintVectorUserA1),
		FirstAssistant: []byte(contentFingerprintVectorAsstA1),
		SecondUser:     []byte(contentFingerprintVectorUserA2),
		MessageCount:   6,
	}
	anchorsAFromScan, ok := ScanConversationAnchors([]byte(contentFingerprintVectorBodyA()))
	if !ok {
		t.Fatal("vector A scan failed")
	}

	// 字面量固定向量（公式钉死）。
	if got := ProvisionalFingerprintHex(contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA, anchorsA); got != contentFingerprintVectorProvisionalA {
		t.Errorf("provisional A = %s, want %s", got, contentFingerprintVectorProvisionalA)
	}
	if got := ConfirmedFingerprintHex(contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA, anchorsA); got != contentFingerprintVectorConfirmedA {
		t.Errorf("confirmed A = %s, want %s", got, contentFingerprintVectorConfirmedA)
	}
	// 扫描产出的锚点与手拼锚点同码（原始字节 canonical）。
	if got := ProvisionalFingerprintHex(contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA, anchorsAFromScan); got != contentFingerprintVectorProvisionalA {
		t.Errorf("provisional A (scanned) = %s, want %s", got, contentFingerprintVectorProvisionalA)
	}
	if got := ConfirmedFingerprintHex(contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA, anchorsAFromScan); got != contentFingerprintVectorConfirmedA {
		t.Errorf("confirmed A (scanned) = %s, want %s", got, contentFingerprintVectorConfirmedA)
	}

	// 设计公式独立重组（不经过实现侧拼接逻辑）。
	wantProvisionalA := testFingerprint("cfp1", contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA,
		testHashSegments(contentFingerprintVectorSystemA, contentFingerprintVectorDevA),
		testHashElement(contentFingerprintVectorUserA1))
	if wantProvisionalA != contentFingerprintVectorProvisionalA {
		t.Errorf("independent provisional A = %s, want literal %s", wantProvisionalA, contentFingerprintVectorProvisionalA)
	}
	wantConfirmedA := testFingerprint("cfp1c", contentFingerprintVectorSysAccountA, contentFingerprintVectorAPIKeyA, contentFingerprintVectorProfileA,
		testHashSegments(contentFingerprintVectorSystemA, contentFingerprintVectorDevA),
		testHashElement(contentFingerprintVectorUserA1),
		testHashElement(contentFingerprintVectorAsstA1),
		testHashElement(contentFingerprintVectorUserA2))
	if wantConfirmedA != contentFingerprintVectorConfirmedA {
		t.Errorf("independent confirmed A = %s, want literal %s", wantConfirmedA, contentFingerprintVectorConfirmedA)
	}

	// 向量 B：无 system（空 S 段 = 空拼接 sha256），仅临时档可计算。
	anchorsB := ConversationAnchors{FirstUser: []byte(contentFingerprintVectorUserB1), MessageCount: 1}
	if got := ProvisionalFingerprintHex(contentFingerprintVectorSysAccountB, contentFingerprintVectorAPIKeyB, contentFingerprintVectorProfileB, anchorsB); got != contentFingerprintVectorProvisionalB {
		t.Errorf("provisional B = %s, want %s", got, contentFingerprintVectorProvisionalB)
	}
	if got := ConfirmedFingerprintHex(contentFingerprintVectorSysAccountB, contentFingerprintVectorAPIKeyB, contentFingerprintVectorProfileB, anchorsB); got != "" {
		t.Errorf("confirmed B = %q, want empty (not computable)", got)
	}
}

// TestContentFingerprintFieldSensitivity 钉死字段敏感性：改任一输入字段或任
// 一锚点字节 → 码变（设计 §9.7）。
func TestContentFingerprintFieldSensitivity(t *testing.T) {
	base := ConversationAnchors{
		SystemElements: [][]byte{[]byte(contentFingerprintVectorSystemA)},
		FirstUser:      []byte(contentFingerprintVectorUserA1),
		FirstAssistant: []byte(contentFingerprintVectorAsstA1),
		SecondUser:     []byte(contentFingerprintVectorUserA2),
		MessageCount:   4,
	}
	baseProvisional := ProvisionalFingerprintHex("sys", "key", "profile", base)
	baseConfirmed := ConfirmedFingerprintHex("sys", "key", "profile", base)
	if baseProvisional == "" || baseConfirmed == "" {
		t.Fatal("base fingerprints empty")
	}

	changed := []struct {
		name            string
		systemAccountID string
		apiKeyID        string
		clientProfile   string
		mutate          func(a *ConversationAnchors)
		// 临时档只消费 hashS/hashU1：S/U1 与身份字段变更 → 临时码变；
		// A1/U2 变更只影响确认档。
		wantProvisionalChanged bool
		wantConfirmedChanged   bool
	}{
		{name: "system account", systemAccountID: "sys2", apiKeyID: "key", clientProfile: "profile", wantProvisionalChanged: true, wantConfirmedChanged: true},
		{name: "api key", systemAccountID: "sys", apiKeyID: "key2", clientProfile: "profile", wantProvisionalChanged: true, wantConfirmedChanged: true},
		{name: "client profile", systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile2", wantProvisionalChanged: true, wantConfirmedChanged: true},
		{
			name:            "system element byte",
			systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile",
			mutate: func(a *ConversationAnchors) {
				a.SystemElements = [][]byte{[]byte(`{"role":"system","content":"You are helpful." }`)}
			},
			wantProvisionalChanged: true,
			wantConfirmedChanged:   true,
		},
		{
			name: "u1 byte", systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile",
			mutate:                 func(a *ConversationAnchors) { a.FirstUser = []byte(`{"role":"user","content":"Hello!"}`) },
			wantProvisionalChanged: true,
			wantConfirmedChanged:   true,
		},
		{
			name: "a1 byte", systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile",
			mutate:                 func(a *ConversationAnchors) { a.FirstAssistant = []byte(`{"role":"assistant","content":"Hi"}"`) },
			wantProvisionalChanged: false,
			wantConfirmedChanged:   true,
		},
		{
			name: "u2 byte", systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile",
			mutate:                 func(a *ConversationAnchors) { a.SecondUser = []byte(`{"role":"user","content":"Second question?"}`) },
			wantProvisionalChanged: false,
			wantConfirmedChanged:   true,
		},
		{
			name: "extra system element changes hashS", systemAccountID: "sys", apiKeyID: "key", clientProfile: "profile",
			mutate: func(a *ConversationAnchors) {
				a.SystemElements = append(a.SystemElements, []byte(`{"role":"developer","content":"d"}`))
			},
			wantProvisionalChanged: true,
			wantConfirmedChanged:   true,
		},
	}
	for _, tt := range changed {
		t.Run(tt.name, func(t *testing.T) {
			anchors := ConversationAnchors{
				SystemElements: append([][]byte{}, base.SystemElements...),
				FirstUser:      base.FirstUser,
				FirstAssistant: base.FirstAssistant,
				SecondUser:     base.SecondUser,
				MessageCount:   base.MessageCount,
			}
			if tt.mutate != nil {
				tt.mutate(&anchors)
			}
			provisional := ProvisionalFingerprintHex(tt.systemAccountID, tt.apiKeyID, tt.clientProfile, anchors)
			confirmed := ConfirmedFingerprintHex(tt.systemAccountID, tt.apiKeyID, tt.clientProfile, anchors)
			if (provisional != baseProvisional) != tt.wantProvisionalChanged {
				t.Errorf("provisional changed = %v, want %v", provisional != baseProvisional, tt.wantProvisionalChanged)
			}
			if (confirmed != baseConfirmed) != tt.wantConfirmedChanged {
				t.Errorf("confirmed changed = %v, want %v", confirmed != baseConfirmed, tt.wantConfirmedChanged)
			}
		})
	}
}
