package port

import "time"

type ManagementAccountTestAccess struct {
	ActorSystemAccountID  string
	ActorRole             string
	FilterSystemAccountID string
}

type ManagementAccountTestSession struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	Message           string     `json:"message,omitempty"`
	LastHeartbeatAt   time.Time  `json:"lastHeartbeatAt"`
	CancelRequestedAt *time.Time `json:"cancelRequestedAt,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type ManagementAccountTestTask struct {
	ID                        string         `json:"id"`
	SessionID                 string         `json:"sessionId,omitempty"`
	AccountID                 string         `json:"accountId"`
	AccountName               string         `json:"accountName"`
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID string         `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              string         `json:"protocolCode,omitempty"`
	ProtocolVersion           string         `json:"protocolVersion,omitempty"`
	Type                      string         `json:"type"`
	Status                    string         `json:"status"`
	Message                   string         `json:"message,omitempty"`
	Model                     string         `json:"model,omitempty"`
	TestEndpointMode          string         `json:"testEndpointMode,omitempty"`
	Result                    map[string]any `json:"result,omitempty"`
	CancelRequested           bool           `json:"cancelRequested,omitempty"`
	CreatedAt                 time.Time      `json:"createdAt"`
	QueuedAt                  time.Time      `json:"queuedAt"`
	StartedAt                 *time.Time     `json:"startedAt,omitempty"`
	FinishedAt                *time.Time     `json:"finishedAt,omitempty"`
	UpdatedAt                 time.Time      `json:"updatedAt"`
}
