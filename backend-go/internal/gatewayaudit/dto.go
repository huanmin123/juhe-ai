package gatewayaudit

// CaptureDTO is the HTTP-independent handoff shape for later queue integration.
// It intentionally contains no raw body, blob reference, database model, or route concern.
type CaptureDTO struct {
	TraceID       string                 `json:"traceId"`
	TrafficSource string                 `json:"trafficSource,omitempty"`
	Method        string                 `json:"method"`
	Path          string                 `json:"path"`
	QueryString   string                 `json:"queryString,omitempty"`
	RequestHeader map[string][]string    `json:"requestHeaders,omitempty"`
	Terminal      Terminal               `json:"terminal"`
	Budget        Snapshot               `json:"budget"`
	Attempts      []AttemptDTO           `json:"attempts"`
	Fragments     []PayloadDescriptorDTO `json:"fragments"`
}

type AttemptDTO struct {
	AttemptIndex       int      `json:"attemptIndex"`
	AccountID          string   `json:"accountId,omitempty"`
	ProviderCode       string   `json:"providerCode,omitempty"`
	UpstreamMethod     string   `json:"upstreamMethod,omitempty"`
	UpstreamURL        string   `json:"upstreamUrl,omitempty"`
	UpstreamStatusCode *int     `json:"upstreamStatusCode,omitempty"`
	DurationMillis     int64    `json:"durationMs,omitempty"`
	Terminal           Terminal `json:"terminal"`
}

// PayloadDescriptorDTO records bounded capture facts only. A later owner may
// use it when implementing the separately reviewed raw payload/blob pipeline.
type PayloadDescriptorDTO struct {
	PartType        string              `json:"partType"`
	SequenceIndex   int                 `json:"sequenceIndex"`
	Headers         map[string][]string `json:"headers,omitempty"`
	ContentType     string              `json:"contentType,omitempty"`
	ContentEncoding string              `json:"contentEncoding,omitempty"`
	RawBodyBytes    int64               `json:"rawBodyBytes,omitempty"`
	BodySHA256      string              `json:"bodySha256,omitempty"`
	CaptureStatus   string              `json:"captureStatus"`
}
