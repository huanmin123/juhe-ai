package port

import "context"

type ManagementModelCheckRun struct {
	ID                         string
	SystemAccountID            string
	ActorSystemAccountID       string
	ProviderCode               string
	TargetType                 string
	TargetID                   string
	TargetName                 *string
	TargetOwnerSystemAccountID *string
	AccountID                  *string
	GroupID                    *string
	APIKeyID                   *string
	Model                      string
	Profile                    string
	TrustedComparison          bool
	TrustedComparisonAvailable bool
	Level                      string
	Score                      int
	MaxScore                   int
	Status                     string
	Message                    string
	TraceID                    *string
	ProbeSetVersion            string
	StartedAt                  string
	FinishedAt                 *string
	DurationMs                 *int
	RequestSummaryJSON         string
	ResultSummaryJSON          string
	ErrorCode                  *string
	ErrorMessage               *string
	CreatedAt                  string
	UpdatedAt                  string
}

type ManagementModelCheckItem struct {
	ID                  string
	RunID               string
	ItemKey             string
	ItemType            string
	Status              string
	Score               int
	MaxScore            int
	DurationMs          *int
	TraceID             *string
	EvidenceSummaryJSON string
	ErrorCode           *string
	ErrorMessage        *string
	CreatedAt           string
	UpdatedAt           string
}

type ManagementModelAccountTrustResult struct {
	IdentityStatus             string
	MappingStatus              string
	UsageIntegrityStatus       string
	ProtocolStatus             string
	EvidenceStatus             string
	EvidenceCoverage           float64
	ObservationCount           int
	RoundCount                 int
	IndependentSourceCount     int
	IdentityObservationCount   int
	PairedProbeCount           int
	Slope                      *float64
	Intercept                  *float64
	InterceptBaselineMedian    *float64
	InterceptBaselineMAD       *float64
	InterceptBaselineVersion   *int
	InterceptBaselineStatus    *string
	InterceptStrongGateEnabled bool
	IdentityDistance           *float64
	PairedDistance             *float64
	PairedBaselineMedian       *float64
	PairedBaselineMAD          *float64
	BaselineVersion            *int
	BaselineVersionStatus      *string
	FeatureVersion             *string
	TokenizerVersion           *string
	ProbeSetVersion            string
	ReasonCodes                []string
	LastObservedAt             *string
}

type ManagementModelCheckRunListInput struct {
	SystemAccountID string
	TargetType      string
	TargetID        string
	Model           string
	Level           string
	Status          string
	StartAt         string
	EndAt           string
	Limit           int
	Offset          int
}

type ManagementModelCheckRunListResult struct {
	Items   []ManagementModelCheckRun
	HasMore bool
}

type ManagementModelCheckReader interface {
	FindManagementModelCheckActive(ctx context.Context, actorSystemAccountID string) (ManagementModelCheckRun, bool, error)
	ListManagementModelCheckRuns(ctx context.Context, input ManagementModelCheckRunListInput) (ManagementModelCheckRunListResult, error)
	GetManagementModelCheckRun(ctx context.Context, id string, systemAccountID string) (ManagementModelCheckRun, []ManagementModelCheckItem, bool, error)
	FindManagementModelAccountTrustResult(ctx context.Context, systemAccountID string, accountID string, requestedModel string) (ManagementModelAccountTrustResult, bool, error)
}
