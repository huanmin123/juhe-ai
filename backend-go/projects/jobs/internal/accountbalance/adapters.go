package accountbalance

import (
	"fmt"
	"math/big"
	"strings"
)

func ParseSub2API(value any) (Snapshot, error) {
	response, err := object(value, "Sub2API 响应")
	if err != nil {
		return Snapshot{}, err
	}
	if response["unit"] != "USD" {
		return Snapshot{}, fmt.Errorf("Sub2API 余额单位必须是 USD")
	}
	rawValue, hasRemaining := response["remaining"]
	if !hasRemaining || rawValue == nil {
		rawValue = response["balance"]
	}
	field := "remaining"
	if !hasRemaining {
		field = "balance"
	}
	remaining, err := parseDecimal(rawValue, field)
	if err != nil {
		return Snapshot{}, err
	}
	_, hasBalance := response["balance"]
	isWallet := response["planName"] == "钱包余额" || hasBalance
	basis := BasisSubscription
	if response["mode"] == "quota_limited" {
		basis = BasisAPIKeyQuota
	} else if isWallet {
		basis = BasisWallet
	}
	if basis == BasisSubscription {
		minusOne := decimal{coefficient: bigOneNeg(remaining.scale), scale: remaining.scale}
		if remaining.coefficient.Cmp(minusOne.coefficient) == 0 {
			return Snapshot{Status: StatusUnlimited, Basis: basis}, nil
		}
	}
	return fresh(remaining, decimalOne(), RawUnitUSD, basis, decimalText(remaining))
}

func ParseNewAPI(value any, quotaPerUnit any) (Snapshot, error) {
	root, err := object(value, "New API 响应")
	if err != nil {
		return Snapshot{}, err
	}
	data, err := object(root["data"], "New API data")
	if err != nil {
		return Snapshot{}, err
	}
	if data["unlimited_quota"] == true {
		return Snapshot{Status: StatusUnsupported, Basis: BasisAPIKeyQuota}, nil
	}
	remaining, err := parseDecimal(data["total_available"], "total_available")
	if err != nil {
		return Snapshot{}, err
	}
	divisor, err := parseDecimal(quotaPerUnit, "quota_per_unit")
	if err != nil {
		return Snapshot{}, err
	}
	return fresh(remaining, divisor, RawUnitQuota, BasisAPIKeyQuota, decimalText(remaining))
}

func ParseOpenAIBilling(subscriptionValue, usageValue any, divisorValue any, unit RawUnit) (Snapshot, error) {
	subscription, err := object(subscriptionValue, "账单订阅响应")
	if err != nil {
		return Snapshot{}, err
	}
	usage, err := object(usageValue, "账单用量响应")
	if err != nil {
		return Snapshot{}, err
	}
	if subscription["object"] != "billing_subscription" {
		return Snapshot{}, fmt.Errorf("账单订阅响应类型不匹配")
	}
	if usage["object"] != "list" {
		return Snapshot{}, fmt.Errorf("账单用量响应类型不匹配")
	}
	hardLimit, err := parseDecimal(subscription["hard_limit_usd"], "hard_limit_usd")
	if err != nil {
		return Snapshot{}, err
	}
	if decimalText(hardLimit) == "100000000" {
		return Snapshot{Status: StatusUnsupported, Basis: BasisAPIKeyQuota, ErrorMessage: "上游 API Key 为无限额度，无法确认实际可用余额"}, nil
	}
	totalUsage, err := parseDecimal(usage["total_usage"], "total_usage")
	if err != nil {
		return Snapshot{}, err
	}
	if totalUsage.coefficient.Sign() < 0 {
		return Snapshot{}, fmt.Errorf("total_usage 不能为负数")
	}
	remaining := decimalSubtract(hardLimit, decimalDivideByHundred(totalUsage))
	divisor := decimalOne()
	if divisorValue != nil {
		divisor, err = parseDecimal(divisorValue, "金额换算系数")
		if err != nil {
			return Snapshot{}, err
		}
	}
	return fresh(remaining, divisor, unit, BasisAPIKeyQuota, decimalText(remaining))
}

func ParseLiteLLM(value any) (Snapshot, error) {
	root, err := object(value, "LiteLLM 响应")
	if err != nil {
		return Snapshot{}, err
	}
	info, err := object(root["info"], "LiteLLM info")
	if err != nil {
		return Snapshot{}, err
	}
	if info["max_budget"] == nil {
		return Snapshot{Status: StatusUnsupported, Basis: BasisBudget}, nil
	}
	maxBudget, err := parseDecimal(info["max_budget"], "max_budget")
	if err != nil {
		return Snapshot{}, err
	}
	spend := info["spend"]
	if spend == nil {
		spend = 0
	}
	spent, err := parseDecimal(spend, "spend")
	if err != nil {
		return Snapshot{}, err
	}
	return fresh(decimalSubtract(maxBudget, spent), decimalOne(), RawUnitUSD, BasisBudget, decimalText(decimalSubtract(maxBudget, spent)))
}

func ParseUserBalance(value any) (Snapshot, error) {
	response, err := object(value, "用户余额响应")
	if err != nil {
		return Snapshot{}, err
	}
	balance, err := parseDecimal(response["balance"], "balance")
	if err != nil {
		return Snapshot{}, err
	}
	return fresh(balance, decimalOne(), RawUnitUSD, BasisWallet, decimalText(balance))
}

// ParseCustom resolves the frozen JSON Pointer form used by J2 custom
// adapters. A pointer is evaluated against an object at every step; missing
// fields and malformed escape sequences fail closed.
func ParseCustom(value any, remainingPointer, totalPointer, usedPointer, divisorValue string) (Snapshot, error) {
	var raw decimal
	var err error
	if remainingPointer != "" {
		resolved, resolveErr := jsonPointer(value, remainingPointer)
		if resolveErr != nil {
			return Snapshot{}, resolveErr
		}
		raw, err = parseDecimal(resolved, "余额字段")
	} else {
		total, resolveErr := jsonPointer(value, totalPointer)
		if resolveErr != nil {
			return Snapshot{}, resolveErr
		}
		used, resolveErr := jsonPointer(value, usedPointer)
		if resolveErr != nil {
			return Snapshot{}, resolveErr
		}
		var totalDecimal, usedDecimal decimal
		totalDecimal, err = parseDecimal(total, "总额字段")
		if err == nil {
			usedDecimal, err = parseDecimal(used, "已用字段")
		}
		if err == nil {
			raw = decimalSubtract(totalDecimal, usedDecimal)
		}
	}
	if err != nil {
		return Snapshot{}, err
	}
	divisor := decimalOne()
	if divisorValue != "" {
		divisor, parseErr := parseDecimal(divisorValue, "divisor")
		if parseErr != nil {
			return Snapshot{}, parseErr
		}
		if divisor.coefficient.Sign() <= 0 {
			return Snapshot{}, fmt.Errorf("金额除数必须是正数")
		}
		return fresh(raw, divisor, RawUnitUSD, BasisCustom, decimalText(raw))
	}
	return fresh(raw, divisor, RawUnitUSD, BasisCustom, decimalText(raw))
}

func jsonPointer(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if pointer[0] != '/' {
		return nil, fmt.Errorf("JSON Pointer 字段不存在：%s", pointer)
	}
	current := value
	for _, token := range strings.Split(pointer[1:], "/") {
		key, err := decodeJSONPointerToken(token)
		if err != nil {
			return nil, fmt.Errorf("JSON Pointer 字段不存在：%s", pointer)
		}
		objectValue, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("JSON Pointer 字段不存在：%s", pointer)
		}
		current, ok = objectValue[key]
		if !ok {
			return nil, fmt.Errorf("JSON Pointer 字段不存在：%s", pointer)
		}
	}
	return current, nil
}

func decodeJSONPointerToken(token string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			builder.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
			return "", fmt.Errorf("invalid JSON Pointer escape")
		}
		if token[i+1] == '0' {
			builder.WriteByte('~')
		} else {
			builder.WriteByte('/')
		}
		i++
	}
	return builder.String(), nil
}

func ParseOpenAIBillingStatus(value any) (BillingStatus, error) {
	response, err := object(value, "余额状态响应")
	if err != nil {
		return BillingStatus{}, err
	}
	if response["success"] != true {
		return BillingStatus{}, fmt.Errorf("上游状态接口未返回成功响应")
	}
	data, err := object(response["data"], "余额状态 data")
	if err != nil {
		return BillingStatus{}, err
	}
	displayType := ""
	if text, ok := data["quota_display_type"].(string); ok {
		displayType = strings.ToUpper(strings.TrimSpace(text))
	}
	switch displayType {
	case "USD":
		return BillingStatus{RawUnit: RawUnitUSD}, nil
	case "CNY":
		rate, err := parseDecimal(data["usd_exchange_rate"], "usd_exchange_rate")
		if err != nil {
			return BillingStatus{}, err
		}
		return BillingStatus{RawUnit: RawUnitCNY, Divisor: decimalText(rate)}, nil
	case "TOKENS", "CUSTOM":
		return BillingStatus{Snapshot: &Snapshot{Status: StatusUnsupported, Basis: BasisAPIKeyQuota, ErrorMessage: fmt.Sprintf("上游余额展示单位为 %s，无法安全换算为美元", displayType)}}, nil
	}
	if data["display_in_currency"] == true {
		return BillingStatus{RawUnit: RawUnitUSD}, nil
	}
	if data["display_in_currency"] == false {
		rate, err := parseDecimal(data["quota_per_unit"], "quota_per_unit")
		if err != nil {
			return BillingStatus{}, err
		}
		return BillingStatus{RawUnit: RawUnitQuota, Divisor: decimalText(rate)}, nil
	}
	return BillingStatus{}, fmt.Errorf("上游状态接口未提供可识别的余额单位")
}

func decimalOne() decimal { return decimal{coefficient: bigOne(0), scale: 0} }

func bigOne(scale int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}

func bigOneNeg(scale int) *big.Int { return new(big.Int).Neg(bigOne(scale)) }
