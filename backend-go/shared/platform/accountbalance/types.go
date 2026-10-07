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

// Balance scope/aggregation carry the multi-Key 口径 contract (Node
// AccountBalanceScope / AccountBalanceAggregation). scope 说明余额数值的归属：
// key=独立 Key 配额（口径一致时可安全合计），account=账户级共享额度（只展示
// 共享值，禁止相加），unknown=上游未表明口径（禁止合计）。
const (
	ScopeKey     string = "key"
	ScopeAccount string = "account"
	ScopeUnknown string = "unknown"
)

// Aggregation 说明合并余额的计算方式：sum=逐 Key 精确求和，shared=共享值原样
// 展示，unknown=无法确定安全口径。单 Key 快照两个口径字段都保持空（不参与
// 序列化）。
const (
	AggregationSum     string = "sum"
	AggregationShared  string = "shared"
	AggregationUnknown string = "unknown"
)

// KeyBalance is one per-Key entry of a multi-Key snapshot. It mirrors Node
// AccountBalanceKeySnapshot / gateway BalanceKeySnapshot (camelCase JSON), so
// the gateway can join stored entries by keyFingerprint and render masked
// per-Key details. The raw API Key never appears here.
type KeyBalance struct {
	KeyFingerprint string  `json:"keyFingerprint"`
	MaskedKey      string  `json:"maskedKey"`
	Status         Status  `json:"status"`
	RemainingUSD   string  `json:"remainingUsd,omitempty"`
	RawUnit        RawUnit `json:"rawUnit,omitempty"`
	Scope          string  `json:"scope,omitempty"`
	Basis          Basis   `json:"basis,omitempty"`
	ErrorMessage   string  `json:"errorMessage,omitempty"`
	LastAttemptAt  string  `json:"lastAttemptAt,omitempty"`
	LastSuccessAt  string  `json:"lastSuccessAt,omitempty"`
}

// Snapshot is the jobs-owned balance result. Amounts are canonical decimal
// strings; using strings avoids float rounding across Node/Go boundaries.
//
// The multi-Key fields (KeyCount/QueriedKeyCount/Scope/Aggregation/KeyBalances)
// are only populated by the multi-Key execution path; the single-Key path
// leaves them zero so existing snapshots keep their exact JSON shape. KeyCount
// therefore also carries omitempty: multi-Key results always have KeyCount>0,
// while single-Key snapshots must not grow a "keyCount":0 field.
type Snapshot struct {
	Status                    Status  `json:"status"`
	// InputDigest 是余额输入身份摘要（BalanceInputDigest）：gateway 读端
	// 以账户当前列值现算同一摘要做显示匹配，替代全局 config_revision 相等
	// 判定（BUG-0286）。J2 写端在 persistInput 统一打点。
	InputDigest               string  `json:"inputDigest,omitempty"`
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
	// Multi-Key 合并结果专用字段（对齐 Node AccountBalanceSnapshot :59-64）。
	KeyCount        int          `json:"keyCount,omitempty"`
	QueriedKeyCount int          `json:"queriedKeyCount,omitempty"`
	Scope           string       `json:"scope,omitempty"`
	Aggregation     string       `json:"aggregation,omitempty"`
	KeyBalances     []KeyBalance `json:"keyBalances,omitempty"`
}

type BillingStatus struct {
	RawUnit  RawUnit
	Divisor  string
	Snapshot *Snapshot
}
