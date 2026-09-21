package accountbalance

// w7c pure unit arms: adapter parsers, decimal math, credential envelopes,
// capability/config validation, model freeze/validation, outcome JSON, and
// runtime config parsing. No database, no network.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func w7cJSONValue(t *testing.T, raw string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestW7CParseSub2APIArms(t *testing.T) {
	if _, err := ParseSub2API("string"); err == nil {
		t.Fatal("non-object must fail")
	}
	if _, err := ParseSub2API(w7cJSONValue(t, `{"unit":"CNY","remaining":1}`)); err == nil || !strings.Contains(err.Error(), "USD") {
		t.Fatalf("non-USD: %v", err)
	}
	if _, err := ParseSub2API(w7cJSONValue(t, `{"unit":"USD","remaining":"x"}`)); err == nil {
		t.Fatal("bad remaining must fail")
	}
	if _, err := ParseSub2API(w7cJSONValue(t, `{"unit":"USD"}`)); err == nil {
		t.Fatal("missing remaining/balance must fail")
	}
	// -1 remaining with subscription basis is the unlimited sentinel.
	snapshot, err := ParseSub2API(w7cJSONValue(t, `{"unit":"USD","remaining":-1}`))
	if err != nil || snapshot.Status != StatusUnlimited || snapshot.Basis != BasisSubscription {
		t.Fatalf("unlimited sentinel: %#v %v", snapshot, err)
	}
	// quota_limited mode overrides the wallet basis.
	snapshot, err = ParseSub2API(w7cJSONValue(t, `{"unit":"USD","remaining":5,"mode":"quota_limited","balance":9}`))
	if err != nil || snapshot.Basis != BasisAPIKeyQuota {
		t.Fatalf("quota basis: %#v %v", snapshot, err)
	}
	// balance fallback marks wallet basis.
	snapshot, err = ParseSub2API(w7cJSONValue(t, `{"unit":"USD","balance":7.25}`))
	if err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "7.250000" || snapshot.Basis != BasisWallet {
		t.Fatalf("balance fallback: %#v %v", snapshot, err)
	}
}

func TestW7CParseNewAPIArms(t *testing.T) {
	if _, err := ParseNewAPI("x", 1); err == nil {
		t.Fatal("non-object root must fail")
	}
	if _, err := ParseNewAPI(w7cJSONValue(t, `{"data":1}`), 1); err == nil {
		t.Fatal("non-object data must fail")
	}
	if snapshot, err := ParseNewAPI(w7cJSONValue(t, `{"data":{"unlimited_quota":true}}`), 1); err != nil || snapshot.Status != StatusUnsupported {
		t.Fatalf("unlimited: %#v %v", snapshot, err)
	}
	if _, err := ParseNewAPI(w7cJSONValue(t, `{"data":{"total_available":"x"}}`), "1"); err == nil {
		t.Fatal("bad total_available must fail")
	}
	if _, err := ParseNewAPI(w7cJSONValue(t, `{"data":{"total_available":10}}`), "zero.."); err == nil {
		t.Fatal("bad quota_per_unit must fail")
	}
	snapshot, err := ParseNewAPI(w7cJSONValue(t, `{"data":{"total_available":250000}}`), json.Number("500000"))
	if err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "0.500000" || snapshot.RawUnit != RawUnitQuota {
		t.Fatalf("fresh quota: %#v %v", snapshot, err)
	}
}

func TestW7CParseOpenAIBillingArms(t *testing.T) {
	subscription := `{"object":"billing_subscription","hard_limit_usd":120}`
	usage := `{"object":"list","total_usage":2000}`
	if _, err := ParseOpenAIBilling("x", w7cJSONValue(t, usage), nil, RawUnitUSD); err == nil {
		t.Fatal("bad subscription must fail")
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, subscription), "x", nil, RawUnitUSD); err == nil {
		t.Fatal("bad usage must fail")
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, `{"object":"other","hard_limit_usd":1}`), w7cJSONValue(t, usage), nil, RawUnitUSD); err == nil {
		t.Fatal("wrong subscription object type must fail")
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, subscription), w7cJSONValue(t, `{"object":"other","total_usage":1}`), nil, RawUnitUSD); err == nil {
		t.Fatal("wrong usage object type must fail")
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, `{"object":"billing_subscription","hard_limit_usd":"x"}`), w7cJSONValue(t, usage), nil, RawUnitUSD); err == nil {
		t.Fatal("bad hard limit must fail")
	}
	snapshot, err := ParseOpenAIBilling(w7cJSONValue(t, `{"object":"billing_subscription","hard_limit_usd":100000000}`), w7cJSONValue(t, usage), nil, RawUnitUSD)
	if err != nil || snapshot.Status != StatusUnsupported || snapshot.ErrorMessage == "" {
		t.Fatalf("infinite quota sentinel: %#v %v", snapshot, err)
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, subscription), w7cJSONValue(t, `{"object":"list","total_usage":-5}`), nil, RawUnitUSD); err == nil || !strings.Contains(err.Error(), "不能为负数") {
		t.Fatalf("negative usage: %v", err)
	}
	if _, err := ParseOpenAIBilling(w7cJSONValue(t, subscription), w7cJSONValue(t, usage), "zero", RawUnitUSD); err == nil {
		t.Fatal("bad divisor must fail")
	}
	snapshot, err = ParseOpenAIBilling(w7cJSONValue(t, subscription), w7cJSONValue(t, usage), "4", RawUnitCNY)
	if err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "25.000000" || snapshot.RawUnit != RawUnitCNY {
		t.Fatalf("divided billing: %#v %v", snapshot, err)
	}
}

func TestW7CParseOpenAIBillingStatusArms(t *testing.T) {
	if _, err := ParseOpenAIBillingStatus("x"); err == nil {
		t.Fatal("non-object must fail")
	}
	if _, err := ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":false}`)); err == nil || !strings.Contains(err.Error(), "未返回成功") {
		t.Fatalf("not success: %v", err)
	}
	if _, err := ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true}`)); err == nil {
		t.Fatal("missing data must fail")
	}
	status, err := ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"quota_display_type":"usd"}}`))
	if err != nil || status.RawUnit != RawUnitUSD || status.Snapshot != nil {
		t.Fatalf("usd display: %#v %v", status, err)
	}
	status, err = ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"quota_display_type":"cny","usd_exchange_rate":7.25}}`))
	if err != nil || status.RawUnit != RawUnitCNY || status.Divisor != "7.25" {
		t.Fatalf("cny display: %#v %v", status, err)
	}
	if _, err := ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"quota_display_type":"cny","usd_exchange_rate":"x"}}`)); err == nil {
		t.Fatal("bad cny rate must fail")
	}
	status, err = ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"quota_display_type":"custom"}}`))
	if err != nil || status.Snapshot == nil || status.Snapshot.Status != StatusUnsupported {
		t.Fatalf("custom display: %#v %v", status, err)
	}
	status, err = ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"display_in_currency":true}}`))
	if err != nil || status.RawUnit != RawUnitUSD {
		t.Fatalf("currency flag true: %#v %v", status, err)
	}
	status, err = ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{"display_in_currency":false,"quota_per_unit":500000}}`))
	if err != nil || status.RawUnit != RawUnitQuota || status.Divisor != "500000" {
		t.Fatalf("currency flag false: %#v %v", status, err)
	}
	if _, err := ParseOpenAIBillingStatus(w7cJSONValue(t, `{"success":true,"data":{}}`)); err == nil || !strings.Contains(err.Error(), "未提供可识别") {
		t.Fatalf("unrecognized unit: %v", err)
	}
}

func TestW7CParseLiteLLMAndUserBalanceArms(t *testing.T) {
	if _, err := ParseLiteLLM("x"); err == nil {
		t.Fatal("non-object must fail")
	}
	if _, err := ParseLiteLLM(w7cJSONValue(t, `{"info":1}`)); err == nil {
		t.Fatal("non-object info must fail")
	}
	snapshot, err := ParseLiteLLM(w7cJSONValue(t, `{"info":{}}`))
	if err != nil || snapshot.Status != StatusUnsupported || snapshot.Basis != BasisBudget {
		t.Fatalf("no budget: %#v %v", snapshot, err)
	}
	if _, err := ParseLiteLLM(w7cJSONValue(t, `{"info":{"max_budget":"x"}}`)); err == nil {
		t.Fatal("bad budget must fail")
	}
	if _, err := ParseLiteLLM(w7cJSONValue(t, `{"info":{"max_budget":10,"spend":"x"}}`)); err == nil {
		t.Fatal("bad spend must fail")
	}
	snapshot, err = ParseLiteLLM(w7cJSONValue(t, `{"info":{"max_budget":10,"spend":null}}`))
	if err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "10.000000" {
		t.Fatalf("null spend: %#v %v", snapshot, err)
	}

	if _, err := ParseUserBalance("x"); err == nil {
		t.Fatal("non-object must fail")
	}
	if _, err := ParseUserBalance(w7cJSONValue(t, `{"balance":"x"}`)); err == nil {
		t.Fatal("bad balance must fail")
	}
	if snapshot, err := ParseUserBalance(w7cJSONValue(t, `{"balance":0}`)); err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "0.000000" {
		t.Fatalf("zero balance: %#v %v", snapshot, err)
	}
}

func TestW7CParseCustomPointerArms(t *testing.T) {
	value := w7cJSONValue(t, `{"data":{"credit":1250,"total":900,"used":400,"deep":{"amount":"3"}}}`)
	if _, err := ParseCustom(value, "/data/absent", "", "", ""); err == nil {
		t.Fatal("missing pointer must fail")
	}
	if _, err := ParseCustom(value, "data/credit", "", "", ""); err == nil {
		t.Fatal("pointer without slash must fail")
	}
	snapshot, err := ParseCustom(value, "/data/deep/~1special", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("escaped missing pointer: %v", err)
	}
	escaped := w7cJSONValue(t, `{"a/b":{"c~d":5}}`)
	snapshot, err = ParseCustom(escaped, "/a~1b/c~0d", "", "", "")
	if err != nil || snapshot.Status != StatusFresh || snapshot.RemainingUSD != "5.000000" {
		t.Fatalf("escaped pointer: %#v %v", snapshot, err)
	}
	if snapshot, err = ParseCustom(value, "/data/credit", "", "", "100"); err != nil || snapshot.RemainingUSD != "12.500000" {
		t.Fatalf("divided custom: %#v %v", snapshot, err)
	}
	if _, err = ParseCustom(value, "/data/credit", "", "", "0"); err == nil || !strings.Contains(err.Error(), "正数") {
		t.Fatalf("zero divisor: %v", err)
	}
	if _, err = ParseCustom(value, "", "/data/total", "/data/absent", ""); err == nil {
		t.Fatal("missing used pointer must fail")
	}
	if snapshot, err = ParseCustom(value, "", "/data/total", "/data/used", ""); err != nil || snapshot.RemainingUSD != "500.000000" {
		t.Fatalf("total/used custom: %#v %v", snapshot, err)
	}
	// total=900 minus credit=1250 goes negative but still parses.
	if snapshot, err = ParseCustom(value, "", "/data/total", "/data/credit", ""); err != nil || snapshot.RemainingUSD != "-350.000000" {
		t.Fatalf("negative remaining: %#v %v", snapshot, err)
	}
}

func TestW7CDecimalContract(t *testing.T) {
	if _, err := parseDecimal(struct{}{}, "f"); err == nil {
		t.Fatal("unsupported type must fail")
	}
	for _, raw := range []any{"", "  ", "-", "1.2.3", "1e5", ".5", "a1", "1a"} {
		if _, err := parseDecimal(raw, "f"); err == nil {
			t.Fatalf("invalid numeric %q must fail", raw)
		}
	}
	if _, err := parseDecimal(true, "f"); err == nil {
		t.Fatal("bool must fail")
	}
	negative, err := parseDecimal("-12.50", "f")
	if err != nil || decimalText(negative) != "-12.5" {
		t.Fatalf("negative parse: %v %v", decimalText(negative), err)
	}
	zeroScale, err := parseDecimal("0", "f")
	if err != nil || decimalText(zeroScale) != "0" {
		t.Fatalf("zero renders as zero: %v %v", decimalText(zeroScale), err)
	}

	left, _ := parseDecimal("1.25", "f")
	right, _ := parseDecimal("2.5", "f")
	if got := decimalText(decimalAdd(left, right)); got != "3.75" {
		t.Fatalf("add: %s", got)
	}
	if got := decimalText(decimalSubtract(left, right)); got != "-1.25" {
		t.Fatalf("subtract: %s", got)
	}
	if got := decimalText(decimalAdd(zeroScale, right)); got != "2.5" {
		t.Fatalf("add zero: %s", got)
	}
	if got := decimalText(decimalDivideByHundred(right)); got != "0.025" {
		t.Fatalf("divide by hundred: %s", got)
	}

	quotient, err := decimalDivideToSix(left, right)
	if err != nil || quotient != "0.500000" {
		t.Fatalf("divide to six: %q %v", quotient, err)
	}
	if _, err := decimalDivideToSix(left, zeroScale); err == nil || !strings.Contains(err.Error(), "正数") {
		t.Fatalf("zero divisor: %v", err)
	}
	half, _ := parseDecimal("0.5", "f")
	one, _ := parseDecimal("1", "f")
	if got, _ := decimalDivideToSix(half, one); got != "0.500000" {
		t.Fatalf("rounding up: %s", got)
	}
	negativeQuotient, err := decimalDivideToSix(negative, one)
	if err != nil || negativeQuotient != "-12.500000" {
		t.Fatalf("negative quotient: %q %v", negativeQuotient, err)
	}
	zeroFraction, err := parseDecimal("0.00", "f")
	if err != nil {
		t.Fatal(err)
	}
	if got := decimalText(zeroFraction); got != "0" {
		t.Fatalf("trailing zero fraction: %s", got)
	}
	if got, err := decimalDivideToSix(zeroScale, one); err != nil || got != "0.000000" {
		t.Fatalf("zero quotient keeps sign: %q %v", got, err)
	}
}

func TestW7CCredentialEnvelopeArms(t *testing.T) {
	secret := "w7c-unit-secret"
	if _, err := EncryptV1Envelope(" ", []byte("x")); err == nil {
		t.Fatal("empty secret encrypt must fail")
	}
	if _, err := DecryptV1Envelope(" ", "v1:a:b:c"); err == nil {
		t.Fatal("empty secret decrypt must fail")
	}
	if _, err := DecryptV1Envelope(secret, "not-an-envelope"); err == nil {
		t.Fatal("bad envelope format must fail")
	}
	if _, err := DecryptV1Envelope(secret, "v1:!!!:!!!:!!!"); err == nil {
		t.Fatal("bad base64 must fail")
	}
	if _, err := DecryptV1Envelope(secret, "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAA:AAAA"); err == nil || !strings.Contains(err.Error(), "长度无效") {
		t.Fatalf("bad tag length: %v", err)
	}
	sealed, err := EncryptV1Envelope(secret, []byte(`{"api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptV1Envelope("wrong", sealed); err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("tampered envelope: %v", err)
	}
	if _, err := NewCredentialEnvelope(secret, " ", map[string]string{}); err == nil {
		t.Fatal("empty kind must fail")
	}
	if _, err := NewCredentialEnvelope(secret, "api_key", make(chan int)); err == nil {
		t.Fatal("unmarshalable value must fail")
	}

	if err := openCredential(secret, CredentialEnvelope{}, "api_key", &struct{}{}); err == nil || !strings.Contains(err.Error(), "不完整") {
		t.Fatalf("incomplete envelope: %v", err)
	}
	if err := openCredential(secret, CredentialEnvelope{Kind: "proxy_url", Ciphertext: sealed}, "api_key", &struct{}{}); err == nil {
		t.Fatal("kind mismatch must fail")
	}
	if err := openCredential(secret, CredentialEnvelope{Kind: "api_key", Ciphertext: sealed}, "", nil); err != nil {
		t.Fatalf("nil target skips unmarshal: %v", err)
	}
	badJSON, err := EncryptV1Envelope(secret, []byte("{nope"))
	if err != nil {
		t.Fatal(err)
	}
	if err := openCredential(secret, CredentialEnvelope{Kind: "api_key", Ciphertext: badJSON}, "api_key", &struct{}{}); err == nil || !strings.Contains(err.Error(), "解析凭据 envelope 失败") {
		t.Fatalf("bad json: %v", err)
	}
}

func TestW7CValidateCapabilityArms(t *testing.T) {
	if _, err := ValidateCapability(CapabilityInput{AuthorizedInstance: true}, true); err == nil || !strings.Contains(err.Error(), "授权实例") {
		t.Fatalf("authorized enabled: %v", err)
	}
	if decision, err := ValidateCapability(CapabilityInput{AuthorizedInstance: true}, false); err != nil || decision.Enabled {
		t.Fatalf("authorized disabled: %#v %v", decision, err)
	}
	if _, err := ValidateCapability(CapabilityInput{Type: "oauth"}, true); err == nil || !strings.Contains(err.Error(), "仅支持 API Key") {
		t.Fatalf("non api key: %v", err)
	}
	decision, err := ValidateCapability(CapabilityInput{
		Type: "api_key", Credentials: map[string]any{"api_keys": []any{"k1", "k2"}},
	}, true)
	if err != nil || !decision.AutoDisabledForMultipleKeys {
		t.Fatalf("multi key: %#v %v", decision, err)
	}
	if decision, err := ValidateCapability(CapabilityInput{Type: "api_key", Credentials: map[string]any{}}, false); err != nil || decision.Enabled {
		t.Fatalf("disabled: %#v %v", decision, err)
	}
	if _, err := ValidateCapability(CapabilityInput{Type: "api_key", Credentials: map[string]any{}}, true); err == nil || !strings.Contains(err.Error(), "一个有效的 API Key") {
		t.Fatalf("no key: %v", err)
	}
	decision, err = ValidateCapability(CapabilityInput{Type: "api_key", Credentials: map[string]any{"api_key": " solo "}}, true)
	if err != nil || !decision.Enabled || decision.AutoDisabledForMultipleKeys {
		t.Fatalf("enabled single key: %#v %v", decision, err)
	}

	// EffectiveAPIKeys variants.
	keys := EffectiveAPIKeys(map[string]any{"api_keys": []string{"a", "a", " b ", ""}})
	if len(keys) != 2 {
		t.Fatalf("string pool dedupe: %#v", keys)
	}
	keys = EffectiveAPIKeys(map[string]any{"api_keys": []any{1, "a"}})
	if len(keys) != 1 {
		t.Fatalf("mixed pool: %#v", keys)
	}
	if keys := EffectiveAPIKeys(map[string]any{"api_key": 42}); len(keys) != 0 {
		t.Fatalf("non-string single key: %#v", keys)
	}
}

func TestW7CNormalizeConfigArms(t *testing.T) {
	if _, err := NormalizeConfig(map[string]any{"mystery": 1}); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": 5}); err == nil {
		t.Fatal("non-string adapter must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "magic"}); err == nil {
		t.Fatal("unknown adapter must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 0.5}); err == nil {
		t.Fatal("fractional interval must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 11}); err == nil {
		t.Fatal("interval above 10 must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "preferredBuiltinAdapter": "magic"}); err == nil {
		t.Fatal("bad preferred must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom"}); err == nil || !strings.Contains(err.Error(), "必须提供查询配置") {
		t.Fatalf("custom without config: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "custom": map[string]any{"path": "/x", "remainingPointer": "/a"}}); err == nil || !strings.Contains(err.Error(), "不能提供自定义配置") {
		t.Fatalf("builtin with custom: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "preferredBuiltinAdapter": "user_balance", "custom": map[string]any{"path": "/x", "remainingPointer": "/a"}}); err == nil || !strings.Contains(err.Error(), "不能提供内置适配偏好") {
		t.Fatalf("custom with preferred: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": "x"}); err == nil {
		t.Fatal("non-map custom must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a", "mystery": 1}}); err == nil {
		t.Fatal("custom unknown field must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "x"}}); err == nil {
		t.Fatal("custom path without slash must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": 1}}); err == nil {
		t.Fatal("non-string pointer must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a~2"}}); err == nil {
		t.Fatal("bad pointer escape must fail")
	}
	// remaining wins over a dangling total pointer; the error arm is a total
	// pointer without remaining and without the used counterpart.
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a", "totalPointer": "/b"}}); err != nil {
		t.Fatalf("remaining plus dangling total: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "totalPointer": "/b"}}); err == nil || !strings.Contains(err.Error(), "必须配置余额") {
		t.Fatalf("total without pair: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a", "divisor": 5}}); err == nil || !strings.Contains(err.Error(), "除数") {
		t.Fatalf("non-string divisor: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "custom", "custom": map[string]any{"path": "/x", "remainingPointer": "/a", "divisor": "-1"}}); err == nil {
		t.Fatal("negative divisor must fail")
	}
	config, err := NormalizeConfig(map[string]any{"adapter": "custom", "intervalMinutes": 7, "custom": map[string]any{"path": " /x ", "remainingPointer": " /a ", "divisor": " 2.5 "}})
	if err != nil || config.IntervalMinutes != 7 || config.Custom.Path != "/x" || config.Custom.Divisor != "2.5" || config.Custom.RemainingPointer != "/a" {
		t.Fatalf("valid custom config: %#v %v", config, err)
	}
}

func TestW7CCandidateToInputArms(t *testing.T) {
	now := time.Now().UTC()
	candidate := w7cBalanceCandidate(t, newStoreNoSchema(), "w7c-toinput", nil)
	if _, err := candidate.ToInput(Trigger("weird"), now, time.Minute); err == nil || !strings.Contains(err.Error(), "trigger 无效") {
		t.Fatalf("bad trigger: %v", err)
	}
	if _, err := candidate.ToInput(TriggerPeriodic, now, 16*time.Minute); err == nil || !strings.Contains(err.Error(), "TTL") {
		t.Fatalf("ttl bound: %v", err)
	}
	noSystem := candidate
	noSystem.SystemAccountID = " "
	if _, err := noSystem.ToInput(TriggerPeriodic, now, time.Minute); err == nil {
		t.Fatal("missing system account must fail")
	}
	oauth := candidate
	oauth.Type = "oauth"
	if _, err := oauth.ToInput(TriggerPeriodic, now, time.Minute); err == nil {
		t.Fatal("non api key must fail")
	}
	noKeys := candidate
	noKeys.APIKeyCount = 0
	noKeys.APIKey.Ciphertext = ""
	if _, err := noKeys.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "一个 API Key") {
		t.Fatalf("no keys: %v", err)
	}
	emptyKind := candidate
	emptyKind.APIKey.Ciphertext = ""
	emptyKind.Credential = CredentialEnvelope{Kind: " ", Ciphertext: " "}
	if _, err := emptyKind.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "加密 API Key") {
		t.Fatalf("empty credential: %v", err)
	}
	noBase := candidate
	noBase.BaseURL = " "
	if _, err := noBase.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "Base URL") {
		t.Fatalf("missing base url: %v", err)
	}
	badBase := candidate
	badBase.BaseURL = "ftp://example.test"
	if _, err := badBase.ToInput(TriggerPeriodic, now, time.Minute); err == nil {
		t.Fatal("non-http base must fail")
	}
	expired := candidate
	expired.ExpiresAt = now.Add(-time.Minute)
	if _, err := expired.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Fatalf("expired candidate: %v", err)
	}
	// First probe may omit the query config and receives the builtin default.
	first := candidate
	first.Config = QueryConfig{}
	input, err := first.ToInput(TriggerFirstProbe, now, time.Minute)
	if err != nil || input.Config.Adapter != Adapter("builtin") || input.Config.IntervalMinutes != 5 {
		t.Fatalf("first probe default config: %#v %v", input.Config, err)
	}
	// Non-first-probe candidates must carry a config.
	emptyConfig := candidate
	emptyConfig.Config = QueryConfig{}
	if _, err := emptyConfig.ToInput(TriggerPeriodic, now, time.Minute); err == nil || !strings.Contains(err.Error(), "缺少查询配置") {
		t.Fatalf("missing config: %v", err)
	}
	// Zero timestamps are filled from now; trailing slashes trimmed.
	trimmed := candidate
	trimmed.IssuedAt = time.Time{}
	trimmed.BaseURL = "https://example.test/"
	input, err = trimmed.ToInput(TriggerPeriodic, time.Time{}, time.Minute)
	if err != nil || input.IssuedAt.IsZero() || input.BaseURL != "https://example.test" {
		t.Fatalf("defaults: %#v %v", input, err)
	}
	// Proxy envelopes are cloned, not shared.
	withProxy := candidate
	withProxy.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: "x"}
	input, err = withProxy.ToInput(TriggerPeriodic, now, time.Minute)
	if err != nil || input.Proxy == withProxy.Proxy {
		t.Fatalf("proxy clone: %#v", input.Proxy)
	}
}

func TestW7CInputValidateArms(t *testing.T) {
	input := w7cValidBalanceInput("w7c-validate")
	input.Trigger = TriggerManual
	if err := input.Validate(time.Time{}); err != nil {
		t.Fatalf("zero now defaults: %v", err)
	}
	noAccount := input
	noAccount.AccountID = ""
	if err := noAccount.Validate(time.Now().UTC()); err == nil {
		t.Fatal("missing account must fail")
	}
	badType := input
	badType.Type = "oauth"
	if err := badType.Validate(time.Now().UTC()); err == nil {
		t.Fatal("bad type must fail")
	}
	noTimes := input
	noTimes.IssuedAt = time.Time{}
	if err := noTimes.Validate(time.Now().UTC()); err == nil {
		t.Fatal("missing times must fail")
	}
	futureIssued := input
	futureIssued.IssuedAt = time.Now().UTC().Add(time.Hour)
	if err := futureIssued.Validate(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "时间窗") {
		t.Fatalf("future issued: %v", err)
	}
	longWindow := input
	longWindow.IssuedAt = time.Now().UTC().Add(-16 * time.Minute)
	if err := longWindow.Validate(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "时间窗") {
		t.Fatalf("long window: %v", err)
	}
	badTrigger := input
	badTrigger.Trigger = Trigger("weird")
	if err := badTrigger.Validate(time.Now().UTC()); err == nil {
		t.Fatal("bad trigger must fail")
	}
	scheduledNoFence := input
	scheduledNoFence.Trigger = TriggerPeriodic
	scheduledNoFence.NextRefreshAt = nil
	if err := scheduledNoFence.Validate(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "next_refresh_at fence") {
		t.Fatalf("scheduled without fence: %v", err)
	}
	scheduledNoFence.Recovery = true
	if err := scheduledNoFence.Validate(time.Now().UTC()); err != nil {
		t.Fatalf("recovery exempts the due fence: %v", err)
	}
	noCredential := input
	noCredential.APIKey.Ciphertext = " "
	noCredential.Credential.Ciphertext = " "
	if err := noCredential.Validate(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "加密 API Key") {
		t.Fatalf("missing credential: %v", err)
	}
	badConfig := input
	badConfig.Config = QueryConfig{Adapter: Adapter("magic"), IntervalMinutes: 5}
	if err := badConfig.Validate(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "查询配置无效") {
		t.Fatalf("bad config: %v", err)
	}
	badBase := input
	badBase.BaseURL = "https://user@example.test"
	if err := badBase.Validate(time.Now().UTC()); err == nil {
		t.Fatal("base with user info must fail")
	}
}

func TestW7COutcomeJSONRoundTripArms(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	due := now.Add(time.Minute)
	outcome := Outcome{OutcomeID: "o", RequestID: "r", AccountID: "a", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, ExpectedNextRefreshSet: true, ExpectedNextRefreshAt: &due}
	encoded, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"expected_next_refresh_at":`) || strings.Contains(string(encoded), `"expected_input"`) {
		t.Fatalf("encoded outcome: %s", encoded)
	}
	var decoded Outcome
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ExpectedNextRefreshSet != true || decoded.ExpectedNextRefreshAt == nil || !decoded.ExpectedNextRefreshAt.Equal(due) {
		t.Fatalf("decoded fence: %#v", decoded)
	}

	// An explicit null due fence survives the round trip as Set=true, nil.
	outcome.ExpectedNextRefreshAt = nil
	encoded, err = json.Marshal(outcome)
	if err != nil || !strings.Contains(string(encoded), `"expected_next_refresh_at":null`) {
		t.Fatalf("null fence: %s %v", encoded, err)
	}
	decoded = Outcome{}
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.ExpectedNextRefreshSet || decoded.ExpectedNextRefreshAt != nil {
		t.Fatalf("decoded null fence: %#v %v", decoded, err)
	}

	// Payload without the fence key leaves Set=false.
	decoded = Outcome{}
	if err := json.Unmarshal([]byte(`{"outcome_id":"o"}`), &decoded); err != nil || decoded.ExpectedNextRefreshSet {
		t.Fatalf("absent fence: %#v %v", decoded, err)
	}
	// Corrupted fence value fails the decode.
	if err := json.Unmarshal([]byte(`{"expected_next_refresh_at":"nope"}`), &decoded); err == nil {
		t.Fatal("bad fence value must fail")
	}
}

func TestW7CLoadRuntimeConfigArms(t *testing.T) {
	// 2026-09-21 起无总开关：未配置环境 = 依赖缺席（合法 Enabled=false）。
	if _, err := LoadRuntimeConfig(w7cEnvGetter(map[string]string{})); err != nil {
		t.Fatalf("unset env must be absent without Postgres: %v", err)
	}

	valid := map[string]string{
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":                    "go",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":                      "w7c",
		"JUHE_AI_ACCOUNT_BALANCE_STORE":                         "postgres",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":                  "postgres://example.test/db",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL":            "postgres://example.test/business",
		"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET":             "secret",
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET":              "0123456789abcdef0123456789abcdef",
		"JUHE_AI_ACCOUNT_BALANCE_SCAN_INTERVAL":                 "10s",
		"JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE":                   "10m",
		"JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE":                 "5m",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_TTL":                     "10m",
		"JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT":                 "5s",
		"JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET":                  "30s",
		"JUHE_AI_ACCOUNT_BALANCE_MAX_RESPONSE_BYTES":            "1024",
		"JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY":               "8",
		"JUHE_AI_ACCOUNT_BALANCE_IO_CONCURRENCY":                "4",
		"JUHE_AI_ACCOUNT_BALANCE_DB_CONCURRENCY":                "2",
		"JUHE_AI_ACCOUNT_BALANCE_DB_QUEUE_SIZE":                 "16",
		"JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE":                    "16",
		"JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE":           "8",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_OPEN_CONNS":       "4",
		"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_IDLE_CONNS":       "2",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_OPEN_CONNS": "4",
		"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS": "2",
	}
	config, err := LoadRuntimeConfig(w7cEnvGetter(valid))
	if err != nil {
		t.Fatal(err)
	}
	if config.OwnerID != "w7c" || config.IOConcurrency != 4 || config.DBQueueSize != 16 || config.MaxResponseBytes != 1024 {
		t.Fatalf("parsed config: %#v", config)
	}

	failureCases := []struct {
		name string
		key  string
		want string
	}{
		{"owner not go", "JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER", "JOBS_OWNER=go"},
		// owner id 缺省回落主机名（2026-09-21 零配置），不再有缺失失败臂。
		{"wrong store", "JUHE_AI_ACCOUNT_BALANCE_STORE", "postgres"},
		{"bad pool open", "JUHE_AI_ACCOUNT_BALANCE_POSTGRES_MAX_OPEN_CONNS", "正整数"},
		{"bad input pool idle", "JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_MAX_IDLE_CONNS", "连接池配置无效"},
		{"missing business url", "JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL", "INPUT_POSTGRES_URL"},
		{"missing credential secret", "JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET", "CREDENTIAL_SECRET"},
		{"short http secret", "JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET", "32"},
		{"bad scan interval", "JUHE_AI_ACCOUNT_BALANCE_SCAN_INTERVAL", "duration"},
		{"bad owner lease", "JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE", "duration"},
		{"bad account lease", "JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE", "duration"},
		{"bad input ttl", "JUHE_AI_ACCOUNT_BALANCE_INPUT_TTL", "duration"},
		{"bad probe timeout", "JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT", "duration"},
		{"bad cycle budget", "JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET", "duration"},
		{"bad response bytes", "JUHE_AI_ACCOUNT_BALANCE_MAX_RESPONSE_BYTES", "1.."},
		{"bad max concurrency", "JUHE_AI_ACCOUNT_BALANCE_MAX_CONCURRENCY", "1..5096"},
		{"bad io concurrency", "JUHE_AI_ACCOUNT_BALANCE_IO_CONCURRENCY", "1..5096"},
		{"bad db concurrency", "JUHE_AI_ACCOUNT_BALANCE_DB_CONCURRENCY", "1..5096"},
		{"bad db queue", "JUHE_AI_ACCOUNT_BALANCE_DB_QUEUE_SIZE", "1..5096"},
		{"bad batch size", "JUHE_AI_ACCOUNT_BALANCE_BATCH_SIZE", "1..5096"},
		{"bad recovery batch", "JUHE_AI_ACCOUNT_BALANCE_RECOVERY_BATCH_SIZE", "1.."},
	}
	for _, tc := range failureCases {
		t.Run(tc.name, func(t *testing.T) {
			broken := map[string]string{}
			for key, value := range valid {
				broken[key] = value
			}
			broken[tc.key] = "__invalid__"
			if tc.name == "owner not go" {
				broken[tc.key] = "node"
			} else if tc.name == "missing owner id" || tc.name == "missing store url" || tc.name == "missing business url" || tc.name == "missing credential secret" {
				broken[tc.key] = ""
			}
			if _, err := LoadRuntimeConfig(w7cEnvGetter(broken)); err == nil || !strings.Contains(err.Error(), tc.want) && !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}

	// Lease ordering constraints: owner > probe, owner > cycle,
	// account > probe, and owner must cover a full batch of probes.
	tight := map[string]string{}
	for key, value := range valid {
		tight[key] = value
	}
	tight["JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE"] = "15s"
	tight["JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT"] = "15s"
	if _, err := LoadRuntimeConfig(w7cEnvGetter(tight)); err == nil || !strings.Contains(err.Error(), "owner lease 必须大于 probe timeout") {
		t.Fatalf("owner lease at probe timeout: %v", err)
	}
	tight["JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE"] = "15s"
	tight["JUHE_AI_ACCOUNT_BALANCE_CYCLE_BUDGET"] = "30s"
	tight["JUHE_AI_ACCOUNT_BALANCE_PROBE_TIMEOUT"] = "5s"
	if _, err := LoadRuntimeConfig(w7cEnvGetter(tight)); err == nil || !strings.Contains(err.Error(), "owner lease 必须大于 cycle budget") {
		t.Fatalf("owner lease below cycle budget: %v", err)
	}
	tight["JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE"] = "10m"
	tight["JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE"] = "4s"
	if _, err := LoadRuntimeConfig(w7cEnvGetter(tight)); err == nil || !strings.Contains(err.Error(), "account lease 必须大于 probe timeout") {
		t.Fatalf("account lease below probe timeout: %v", err)
	}
	tight["JUHE_AI_ACCOUNT_BALANCE_ACCOUNT_LEASE"] = "5m"
	tight["JUHE_AI_ACCOUNT_BALANCE_OWNER_LEASE"] = "60s"
	tight["JUHE_AI_ACCOUNT_BALANCE_IO_CONCURRENCY"] = "1"
	if _, err := LoadRuntimeConfig(w7cEnvGetter(tight)); err == nil || !strings.Contains(err.Error(), "最坏 probe") {
		t.Fatalf("lease below batch worst case: %v", err)
	}
}

func w7cEnvGetter(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// newStoreNoSchema returns an empty store shell for pure helper tests.
func newStoreNoSchema() *Store { return &Store{} }

func TestW7CIntegerValueAndFreshDivisorArms(t *testing.T) {
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": int64(7)}); err != nil {
		t.Fatalf("int64 interval: %v", err)
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": int64(1) << 40}); err == nil {
		t.Fatal("out-of-range int64 interval must fail")
	}
	if _, err := NormalizeConfig(map[string]any{"adapter": "builtin", "intervalMinutes": 5.5}); err == nil {
		t.Fatal("fractional float interval must fail")
	}
	// A zero quota_per_unit reaches fresh() and fails the positive-divisor
	// contract inside decimalDivideToSix.
	if _, err := ParseNewAPI(w7cJSONValue(t, `{"data":{"total_available":10}}`), "0"); err == nil || !strings.Contains(err.Error(), "正数") {
		t.Fatalf("zero divisor: %v", err)
	}
}
