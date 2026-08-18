package accountbalance

// Adapter identifies a frozen J2 balance protocol. It is intentionally kept
// local to jobs; balance refresh is not a gateway protocol.
type Adapter string

const (
	AdapterSub2API       Adapter = "sub2api"
	AdapterNewAPI        Adapter = "newapi"
	AdapterOpenAIBilling Adapter = "openai_billing"
	AdapterLiteLLM       Adapter = "litellm"
	AdapterUserBalance   Adapter = "user_balance"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusRefreshing  Status = "refreshing"
	StatusFresh       Status = "fresh"
	StatusUnlimited   Status = "unlimited"
	StatusUnsupported Status = "unsupported"
	StatusFailed      Status = "failed"
)

type RawUnit string

const (
	RawUnitUSD   RawUnit = "usd"
	RawUnitCNY   RawUnit = "cny"
	RawUnitQuota RawUnit = "quota"
)

type Basis string

const (
	BasisAPIKeyQuota  Basis = "api_key_quota"
	BasisBudget       Basis = "budget"
	BasisSubscription Basis = "subscription"
	BasisWallet       Basis = "wallet"
	BasisCustom       Basis = "custom"
)

// Snapshot is the jobs-owned balance result. Amounts are canonical decimal
// strings; using strings avoids float rounding across Node/Go boundaries.
type Snapshot struct {
	Status                    Status  `json:"status"`
	RemainingUSD              string  `json:"remainingUsd,omitempty"`
	RawRemaining              string  `json:"rawRemaining,omitempty"`
	RawUnit                   RawUnit `json:"rawUnit,omitempty"`
	Basis                     Basis   `json:"basis,omitempty"`
	ErrorMessage              string  `json:"errorMessage,omitempty"`
	LastAttemptAt             string  `json:"lastAttemptAt,omitempty"`
	LastSuccessAt             string  `json:"lastSuccessAt,omitempty"`
	ConsecutiveTransientFails int     `json:"consecutiveTransientFailures,omitempty"`
	LastTransientErrorMessage string  `json:"lastTransientErrorMessage,omitempty"`
	LastTransientFailureAt    string  `json:"lastTransientFailureAt,omitempty"`
}

type BillingStatus struct {
	RawUnit  RawUnit
	Divisor  string
	Snapshot *Snapshot
}
