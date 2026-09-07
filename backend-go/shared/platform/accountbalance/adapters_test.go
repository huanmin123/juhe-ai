package accountbalance

import (
	"encoding/json"
	"testing"
)

func TestParseSub2APIUnlimitedAndWallet(t *testing.T) {
	unlimited, err := ParseSub2API(map[string]any{"unit": "USD", "remaining": "-1", "planName": "Pro"})
	if err != nil {
		t.Fatal(err)
	}
	if unlimited.Status != StatusUnlimited || unlimited.Basis != BasisSubscription {
		t.Fatalf("unexpected unlimited snapshot: %#v", unlimited)
	}
	wallet, err := ParseSub2API(map[string]any{"unit": "USD", "balance": "12.3456789", "planName": "钱包余额"})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Status != StatusFresh || wallet.RemainingUSD != "12.345679" || wallet.RawRemaining != "12.3456789" || wallet.Basis != BasisWallet {
		t.Fatalf("unexpected wallet snapshot: %#v", wallet)
	}
}

func TestParseNewAPIAndLiteLLM(t *testing.T) {
	newAPI, err := ParseNewAPI(map[string]any{"data": map[string]any{"total_available": "12345"}}, "1000")
	if err != nil {
		t.Fatal(err)
	}
	if newAPI.RemainingUSD != "12.345000" || newAPI.RawUnit != RawUnitQuota {
		t.Fatalf("unexpected New API snapshot: %#v", newAPI)
	}
	unlimited, err := ParseNewAPI(map[string]any{"data": map[string]any{"unlimited_quota": true}}, "1000")
	if err != nil {
		t.Fatal(err)
	}
	if unlimited.Status != StatusUnsupported {
		t.Fatalf("unexpected New API unlimited: %#v", unlimited)
	}
	lite, err := ParseLiteLLM(map[string]any{"info": map[string]any{"max_budget": "20", "spend": "1.25"}})
	if err != nil {
		t.Fatal(err)
	}
	if lite.RemainingUSD != "18.750000" || lite.Basis != BasisBudget {
		t.Fatalf("unexpected LiteLLM snapshot: %#v", lite)
	}
}

func TestParseOpenAIBillingAndStatus(t *testing.T) {
	snapshot, err := ParseOpenAIBilling(
		map[string]any{"object": "billing_subscription", "hard_limit_usd": "20"},
		map[string]any{"object": "list", "total_usage": "125"},
		nil,
		RawUnitUSD,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RemainingUSD != "18.750000" {
		t.Fatalf("unexpected billing snapshot: %#v", snapshot)
	}
	status, err := ParseOpenAIBillingStatus(map[string]any{"success": true, "data": map[string]any{"quota_display_type": "CNY", "usd_exchange_rate": "7.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if status.RawUnit != RawUnitCNY || status.Divisor != "7.2" {
		t.Fatalf("unexpected billing status: %#v", status)
	}
	unsupported, err := ParseOpenAIBillingStatus(map[string]any{"success": true, "data": map[string]any{"quota_display_type": "TOKENS"}})
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Snapshot == nil || unsupported.Snapshot.Status != StatusUnsupported {
		t.Fatalf("unexpected token status: %#v", unsupported)
	}
}

func TestParseCustomPayloadAndJSONNumbers(t *testing.T) {
	payload := map[string]any{"total": json.Number("10.25"), "used": json.Number("2.5")}
	total, err := parseDecimal(payload["total"], "total")
	if err != nil {
		t.Fatal(err)
	}
	used, err := parseDecimal(payload["used"], "used")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fresh(decimalSubtract(total, used), decimalOne(), RawUnitUSD, BasisCustom, decimalText(decimalSubtract(total, used)))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RemainingUSD != "7.750000" {
		t.Fatalf("unexpected custom snapshot: %#v", snapshot)
	}
	custom, err := ParseCustom(map[string]any{"data": map[string]any{"left/balance": "3.5"}}, "/data/left~1balance", "", "", "2")
	if err != nil {
		t.Fatal(err)
	}
	if custom.RemainingUSD != "1.750000" || custom.Basis != BasisCustom {
		t.Fatalf("unexpected pointer snapshot: %#v", custom)
	}
	if _, err := ParseCustom(map[string]any{}, "/missing", "", "", ""); err == nil {
		t.Fatal("missing JSON Pointer must fail closed")
	}
	if _, err := ParseUserBalance(map[string]any{"balance": "not-a-number"}); err == nil {
		t.Fatal("invalid balance must fail closed")
	}
}
