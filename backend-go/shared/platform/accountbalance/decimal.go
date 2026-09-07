package accountbalance

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

type decimal struct {
	coefficient *big.Int
	scale       int
}

func parseDecimal(value any, field string) (decimal, error) {
	var text string
	switch v := value.(type) {
	case string:
		text = strings.TrimSpace(v)
	case json.Number:
		text = string(v)
	case float64:
		text = fmt.Sprintf("%v", v)
	case float32:
		text = fmt.Sprintf("%v", v)
	case int:
		text = fmt.Sprintf("%d", v)
	case int64:
		text = fmt.Sprintf("%d", v)
	default:
		return decimal{}, fmt.Errorf("%s 不是有效数字", field)
	}
	if text == "" {
		return decimal{}, fmt.Errorf("%s 不是有效数字", field)
	}
	negative := false
	if text[0] == '-' {
		negative = true
		text = text[1:]
	}
	if text == "" {
		return decimal{}, fmt.Errorf("%s 不是有效数字", field)
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || parts[0] == "" || strings.Trim(parts[0], "0123456789") != "" {
		return decimal{}, fmt.Errorf("%s 不是有效数字", field)
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if strings.Trim(fraction, "0123456789") != "" {
			return decimal{}, fmt.Errorf("%s 不是有效数字", field)
		}
	}
	digits := strings.TrimLeft(parts[0]+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(digits, 10); !ok {
		return decimal{}, fmt.Errorf("%s 不是有效数字", field)
	}
	if negative {
		coefficient.Neg(coefficient)
	}
	return decimal{coefficient: coefficient, scale: len(fraction)}, nil
}

func decimalText(value decimal) string {
	negative := value.coefficient.Sign() < 0
	coefficient := new(big.Int).Abs(value.coefficient).String()
	if value.scale == 0 {
		if negative && coefficient != "0" {
			return "-" + coefficient
		}
		return coefficient
	}
	if len(coefficient) <= value.scale {
		coefficient = strings.Repeat("0", value.scale+1-len(coefficient)) + coefficient
	}
	integer := coefficient[:len(coefficient)-value.scale]
	fraction := strings.TrimRight(coefficient[len(coefficient)-value.scale:], "0")
	result := integer
	if fraction != "" {
		result += "." + fraction
	}
	if negative && result != "0" {
		return "-" + result
	}
	return result
}

func decimalAdd(left, right decimal) decimal { return decimalBinary(left, right, true) }

func decimalSubtract(left, right decimal) decimal { return decimalBinary(left, right, false) }

func decimalBinary(left, right decimal, add bool) decimal {
	scale := left.scale
	if right.scale > scale {
		scale = right.scale
	}
	l := new(big.Int).Mul(left.coefficient, tenPow(scale-left.scale))
	r := new(big.Int).Mul(right.coefficient, tenPow(scale-right.scale))
	if add {
		l.Add(l, r)
	} else {
		l.Sub(l, r)
	}
	return decimal{coefficient: l, scale: scale}
}

func decimalDivideByHundred(value decimal) decimal {
	return decimal{coefficient: new(big.Int).Set(value.coefficient), scale: value.scale + 2}
}

func decimalDivideToSix(value, divisor decimal) (string, error) {
	if divisor.coefficient.Sign() <= 0 {
		return "", fmt.Errorf("金额除数必须是正数")
	}
	numerator := new(big.Int).Abs(value.coefficient)
	numerator.Mul(numerator, tenPow(divisor.scale+6))
	denominator := new(big.Int).Mul(divisor.coefficient, tenPow(value.scale))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	digits := quotient.String()
	if len(digits) < 7 {
		digits = strings.Repeat("0", 7-len(digits)) + digits
	}
	text := digits[:len(digits)-6] + "." + digits[len(digits)-6:]
	if value.coefficient.Sign() < 0 && quotient.Sign() != 0 {
		text = "-" + text
	}
	return text, nil
}

func tenPow(scale int) *big.Int {
	if scale <= 0 {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}

func object(value any, field string) (map[string]any, error) {
	result, ok := value.(map[string]any)
	if !ok || result == nil {
		return nil, fmt.Errorf("%s 必须是 JSON 对象", field)
	}
	return result, nil
}

func fresh(value decimal, divisor decimal, unit RawUnit, basis Basis, raw string) (Snapshot, error) {
	remaining, err := decimalDivideToSix(value, divisor)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Status: StatusFresh, RemainingUSD: remaining, RawRemaining: raw, RawUnit: unit, Basis: basis}, nil
}
