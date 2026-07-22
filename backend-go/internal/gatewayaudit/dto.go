package gatewayaudit

import "strings"

const (
	maxTraceIDBytes       = 256
	maxTrafficSourceBytes = 128
	maxMethodBytes        = 32
	maxPathBytes          = 4096
	maxIdentifierBytes    = 512
	maxURLBytes           = 8192
	maxContentTypeBytes   = 512
	maxDiagnosticBytes    = 4096
)

// CaptureDTO is the HTTP-independent handoff shape for later queue integration.
// Preview fields are explicitly sanitized and are not raw audit payloads.
type CaptureDTO struct {
	Metadata  MetadataDTO             `json:"metadata"`
	Terminal  Terminal                `json:"terminal"`
	Attempts  []AttemptDTO            `json:"attempts"`
	Fragments []FragmentDescriptorDTO `json:"fragments"`
}

type MetadataDTO struct {
	TraceID                       string              `json:"traceId"`
	TrafficSource                 string              `json:"trafficSource,omitempty"`
	Method                        string              `json:"method"`
	Path                          string              `json:"path"`
	SanitizedRequestHeaderPreview map[string][]string `json:"sanitizedRequestHeaderPreview,omitempty"`
	HeaderPreviewTruncated        bool                `json:"headerPreviewTruncated,omitempty"`
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

type AttemptInput struct {
	AttemptIndex       int
	AccountID          string
	ProviderCode       string
	UpstreamMethod     string
	UpstreamURL        string
	UpstreamStatusCode *int
	DurationMillis     int64
	Terminal           TerminalInput
}

// FragmentDescriptorDTO records bounded capture facts only. It never contains
// raw headers or body bytes; those belong to the separately reviewed payload owner.
type FragmentDescriptorDTO struct {
	PartType               string              `json:"partType"`
	SequenceIndex          int                 `json:"sequenceIndex"`
	SanitizedHeaderPreview map[string][]string `json:"sanitizedHeaderPreview,omitempty"`
	HeaderPreviewTruncated bool                `json:"headerPreviewTruncated,omitempty"`
	ContentType            string              `json:"contentType,omitempty"`
	ContentEncoding        string              `json:"contentEncoding,omitempty"`
	RawBodyBytes           int64               `json:"rawBodyBytes,omitempty"`
	BodySHA256             string              `json:"bodySha256,omitempty"`
	CaptureStatus          string              `json:"captureStatus"`
}

func normalizeMetadata(input MetadataDTO) MetadataDTO {
	headers, headersTruncated := SanitizeHeaderPreview(input.SanitizedRequestHeaderPreview)
	path, _, _ := strings.Cut(input.Path, "?")
	return MetadataDTO{
		TraceID:                       bound(input.TraceID, maxTraceIDBytes),
		TrafficSource:                 bound(input.TrafficSource, maxTrafficSourceBytes),
		Method:                        bound(input.Method, maxMethodBytes),
		Path:                          bound(path, maxPathBytes),
		SanitizedRequestHeaderPreview: headers,
		HeaderPreviewTruncated:        input.HeaderPreviewTruncated || headersTruncated,
	}
}

func normalizeAttempt(input AttemptInput) (AttemptDTO, error) {
	output := AttemptDTO{
		AttemptIndex:       input.AttemptIndex,
		AccountID:          bound(input.AccountID, maxIdentifierBytes),
		ProviderCode:       bound(input.ProviderCode, maxIdentifierBytes),
		UpstreamMethod:     bound(input.UpstreamMethod, maxMethodBytes),
		UpstreamURL:        input.UpstreamURL,
		UpstreamStatusCode: input.UpstreamStatusCode,
		DurationMillis:     input.DurationMillis,
		Terminal:           normalizeTerminal(ResolveTerminal(input.Terminal)),
	}
	if output.UpstreamURL != "" {
		var err error
		output.UpstreamURL, err = SanitizeURL(output.UpstreamURL)
		if err != nil {
			return AttemptDTO{}, err
		}
		output.UpstreamURL = bound(output.UpstreamURL, maxURLBytes)
	}
	if output.DurationMillis < 0 {
		output.DurationMillis = 0
	}
	if output.UpstreamStatusCode != nil {
		status := *output.UpstreamStatusCode
		output.UpstreamStatusCode = &status
	}
	return output, nil
}

func normalizeFragment(input FragmentDescriptorDTO) FragmentDescriptorDTO {
	input.PartType = bound(input.PartType, maxIdentifierBytes)
	var headersTruncated bool
	input.SanitizedHeaderPreview, headersTruncated = SanitizeHeaderPreview(input.SanitizedHeaderPreview)
	input.HeaderPreviewTruncated = input.HeaderPreviewTruncated || headersTruncated
	input.ContentType = bound(input.ContentType, maxContentTypeBytes)
	input.ContentEncoding = bound(input.ContentEncoding, maxIdentifierBytes)
	input.BodySHA256 = bound(input.BodySHA256, 64)
	input.CaptureStatus = bound(input.CaptureStatus, maxIdentifierBytes)
	if input.RawBodyBytes < 0 {
		input.RawBodyBytes = 0
	}
	return input
}

func normalizeTerminal(input Terminal) Terminal {
	input.ErrorPhase = bound(input.ErrorPhase, maxIdentifierBytes)
	input.ErrorCode = bound(input.ErrorCode, maxIdentifierBytes)
	input.ErrorMessage = bound(input.ErrorMessage, maxDiagnosticBytes)
	return input
}

func estimateMetadataBytes(input MetadataDTO) int64 {
	return ResidentItemOverheadBytes + stringBytes(input.TraceID, input.TrafficSource, input.Method, input.Path) + headerBytes(input.SanitizedRequestHeaderPreview)
}

func estimateAttemptBytes(input AttemptDTO) int64 {
	return ResidentItemOverheadBytes + stringBytes(input.AccountID, input.ProviderCode, input.UpstreamMethod, input.UpstreamURL) + estimateTerminalBytes(input.Terminal)
}

func estimateFragmentBytes(input FragmentDescriptorDTO) int64 {
	return ResidentItemOverheadBytes + stringBytes(input.PartType, input.ContentType, input.ContentEncoding, input.BodySHA256, input.CaptureStatus) + headerBytes(input.SanitizedHeaderPreview)
}

func estimateTerminalBytes(input Terminal) int64 {
	return stringBytes(string(input.Outcome), input.ErrorPhase, input.ErrorCode, input.ErrorMessage)
}

func stringBytes(values ...string) int64 {
	var total int64
	for _, value := range values {
		total = saturatingAdd(total, int64(len(value)))
	}
	return total
}

func headerBytes(headers map[string][]string) int64 {
	var total int64
	for name, values := range headers {
		total = saturatingAdd(total, ResidentItemOverheadBytes)
		total = saturatingAdd(total, int64(len(name)))
		for _, value := range values {
			total = saturatingAdd(total, int64(len(value)))
		}
	}
	return total
}

func cloneCaptureDTO(input CaptureDTO) CaptureDTO {
	output := input
	output.Metadata.SanitizedRequestHeaderPreview = cloneHeaders(input.Metadata.SanitizedRequestHeaderPreview)
	output.Attempts = append([]AttemptDTO(nil), input.Attempts...)
	for index := range output.Attempts {
		if input.Attempts[index].UpstreamStatusCode != nil {
			status := *input.Attempts[index].UpstreamStatusCode
			output.Attempts[index].UpstreamStatusCode = &status
		}
	}
	output.Fragments = append([]FragmentDescriptorDTO(nil), input.Fragments...)
	for index := range output.Fragments {
		output.Fragments[index].SanitizedHeaderPreview = cloneHeaders(input.Fragments[index].SanitizedHeaderPreview)
	}
	return output
}

func cloneHeaders(input map[string][]string) map[string][]string {
	if input == nil {
		return nil
	}
	output := make(map[string][]string, len(input))
	for name, values := range input {
		output[name] = append([]string(nil), values...)
	}
	return output
}

func bound(value string, maxBytes int) string {
	bounded, _ := BoundUTF8(value, maxBytes)
	return bounded
}
