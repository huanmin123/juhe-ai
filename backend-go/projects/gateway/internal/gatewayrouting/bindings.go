package gatewayrouting

import (
	"errors"
	"strings"
)

// NormalizeAPIKeyGroupBindingWeight mirrors domain/api-key-routing.ts
// normalizeApiKeyGroupBindingWeight: undefined/null weight defaults to 1 and
// anything outside 1..100 (non-integer weights cannot be expressed in Go, so
// only the range check remains) fails with the original Chinese message.
func NormalizeAPIKeyGroupBindingWeight(value *int64) (int64, error) {
	if value == nil {
		return 1, nil
	}
	if *value < 1 || *value > 100 {
		return 0, errors.New("策略路由分组权重必须是 1-100 之间的整数")
	}
	return *value, nil
}

// compareBindingOrderByPriority mirrors the normalize-time sort tiebreak and
// compareBindingOrder: priority ascending, then group_id ascending.
//
// Node orders group ids with String.prototype.localeCompare under the ICU
// default locale. Bindings carry generated ids of the form grp_<hex>
// (lowercase letters plus digits), whose ICU order is identical to byte-wise
// order, so plain string comparison is byte-identical for every real input.
func compareBindingOrderByPriority(left, right GroupBindingRow) int {
	if left.Priority != right.Priority {
		if left.Priority < right.Priority {
			return -1
		}
		return 1
	}
	return strings.Compare(left.GroupID, right.GroupID)
}
