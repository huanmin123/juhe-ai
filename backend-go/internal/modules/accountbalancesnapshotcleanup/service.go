package accountbalancesnapshotcleanup

import "time"

type Reason string

const (
	ReasonBalanceConfigurationChanged Reason = "balance_configuration_changed"
	ReasonMultipleAPIKeys             Reason = "multiple_api_keys"
	ReasonBatchMultipleAPIKeys        Reason = "batch_multiple_api_keys"
	ReasonBatchBalanceIdentityChanged Reason = "batch_balance_identity_changed"
)

type SuppressionRead struct {
	ConfigurationNextRefreshAt string
	SnapshotNextRefreshAfter   string
	SnapshotUpdatedAt          time.Time
	HasSnapshot                bool
}

func IsSuppressed(updatedBefore time.Time, current SuppressionRead) bool {
	if updatedBefore.IsZero() || !current.HasSnapshot {
		return true
	}
	if current.ConfigurationNextRefreshAt != current.SnapshotNextRefreshAfter {
		return true
	}
	return !current.SnapshotUpdatedAt.After(updatedBefore)
}

func IsValidReason(reason Reason) bool {
	switch reason {
	case ReasonBalanceConfigurationChanged,
		ReasonMultipleAPIKeys,
		ReasonBatchMultipleAPIKeys,
		ReasonBatchBalanceIdentityChanged:
		return true
	default:
		return false
	}
}
