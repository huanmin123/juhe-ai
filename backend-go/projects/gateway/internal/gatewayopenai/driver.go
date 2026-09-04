package gatewayopenai

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// DriverID mirrors the Node driver id.
const DriverID = "openai-v1"

// Driver is the OpenAI v1 gateway protocol driver (openAIV1ProtocolDriver).
type Driver struct{}

// NewDriver builds the driver.
func NewDriver() *Driver { return &Driver{} }

// DefaultRegistry returns a registry with the OpenAI driver registered.
func DefaultRegistry() *gatewayproto.Registry {
	return gatewayproto.NewRegistry(NewDriver())
}

// ---- identity ----

// ID implements ProtocolDriver.
func (d *Driver) ID() string { return DriverID }

// ProtocolCode implements ProtocolDriver.
func (d *Driver) ProtocolCode() string { return ProtocolCode }

// ProtocolVersion implements ProtocolDriver.
func (d *Driver) ProtocolVersion() string { return ProtocolVersion }

// ResponseProtocol implements ProtocolDriver.
func (d *Driver) ResponseProtocol() string { return ResponseProtocol }

// ClientErrorProtocol implements ProtocolDriver.
func (d *Driver) ClientErrorProtocol() string { return ClientErrorProtocol }

// DefaultClientProfile implements ProtocolDriver.
func (d *Driver) DefaultClientProfile() string { return DefaultClientProfile }

// ---- selection ----

// SupportsProfile mirrors isOpenAIProtocolProfile.
func (d *Driver) SupportsProfile(profile gatewayproto.ProtocolProfile) bool {
	return gatewayproto.NormalizeProtocolToken(profile.ProtocolCode) == ProtocolCode &&
		gatewayproto.NormalizeProtocolToken(profile.ProtocolVersion) == ProtocolVersion
}

// MatchPath mirrors the openai branch of gatewayProtocolDriverForRequest.
func (d *Driver) MatchPath(shape gatewayproto.RequestShape) bool {
	endpoint := shape.OriginalPathAndQuery
	if endpoint == "" {
		endpoint = shape.Path
	}
	return IsProtocolRequestPath(endpoint)
}

// EndpointModeForRequestShape mirrors openAIEndpointModeForRequestShape.
func (d *Driver) EndpointModeForRequestShape(shape gatewayproto.RequestShape) (gatewayproto.EndpointMode, bool) {
	return endpointModeForShape(shape.Path, shape.Stream)
}

// ---- usage ----

// ExtractUsageFromJSONBuffer implements ProtocolDriver.
func (d *Driver) ExtractUsageFromJSONBuffer(body []byte) gatewayproto.ParsedUsage {
	return ParseUsageFromJSONBuffer(body)
}

// ExtractUsageFromJSONValue implements ProtocolDriver.
func (d *Driver) ExtractUsageFromJSONValue(value any) gatewayproto.ParsedUsage {
	return ParseUsageFromJSONValue(value)
}

// ExtractUsageFromJSONTextFragment implements ProtocolDriver.
func (d *Driver) ExtractUsageFromJSONTextFragment(text string) gatewayproto.ParsedUsage {
	return ParseUsageFromJSONTextFragment(text)
}

// ---- errors ----

// ParseErrorPayload implements ProtocolDriver.
func (d *Driver) ParseErrorPayload(bodyText string, header http.Header) gatewayproto.ErrorPayload {
	return ParseErrorPayload(bodyText, header)
}

// ---- stream ----

// NewStreamInspector implements ProtocolDriver.
func (d *Driver) NewStreamInspector() gatewayproto.StreamInspector {
	return NewStreamInspector()
}

// ---- request transform ----

// BuildUpstreamRequest implements ProtocolDriver: client request -> upstream
// request (model mapping + URL normalization + lane detection).
func (d *Driver) BuildUpstreamRequest(input gatewayproto.BuildUpstreamRequestInput) (*gatewayproto.BuildUpstreamRequestResult, error) {
	method := input.Method
	if method == "" {
		method = http.MethodPost
	}
	path, _ := SplitPathAndQuery(input.ClientPathAndQuery)
	if path == "" {
		path = "/"
	}

	parsedBody := input.ParsedBody
	if !input.ParsedBodyAvailable {
		parsedBody = parseJSONBodyBytes(input.Body)
	}
	root, _ := parsedBody.(map[string]any)
	requestedModel, _ := root["model"].(string)
	stream := false
	if value, ok := root["stream"].(bool); ok {
		stream = value
	}

	lane := ResolveRequestLane(input.ClientPathAndQuery, parsedBody, requestedModel)

	body := input.Body
	upstreamModel := requestedModel
	if input.ModelMapping != nil {
		transformed, err := buildModelMappedJSONBody(root, input.Body, input.ModelMapping.UpstreamModel)
		if err != nil {
			return nil, err
		}
		body = transformed
		upstreamModel = input.ModelMapping.UpstreamModel
	}

	upstreamPathAndQuery := input.ClientPathAndQuery
	if input.ModelMapping != nil {
		if rewritten, ok := modelMappedUpstreamPathAndQuery(input.ClientPathAndQuery, input.ModelMapping); ok {
			upstreamPathAndQuery = rewritten
		}
	}

	mode, _ := endpointModeForShape(path, stream)
	header := http.Header{}
	for key, values := range input.Header {
		header[key] = append([]string(nil), values...)
	}

	return &gatewayproto.BuildUpstreamRequestResult{
		Method:        method,
		URL:           BuildUpstreamURL(input.UpstreamBaseURL, upstreamPathAndQuery),
		PathAndQuery:  upstreamPathAndQuery,
		Header:        header,
		Body:          body,
		Stream:        stream,
		EndpointMode:  mode,
		Lane:          lane,
		UpstreamModel: upstreamModel,
	}, nil
}

// buildModelMappedJSONBody mirrors buildOpenAIModelMappedJsonBody: the body
// must be a valid JSON object; model is overwritten with the upstream model.
func buildModelMappedJSONBody(root map[string]any, rawBody []byte, upstreamModel string) ([]byte, error) {
	if root != nil {
		next := make(map[string]any, len(root)+1)
		for key, value := range root {
			next[key] = value
		}
		next["model"] = upstreamModel
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, &gatewayproto.BuildUpstreamError{
				Code:    gatewayproto.ErrCodeModelMappingRequestInvalid,
				Message: "账号模型映射要求请求体是 JSON 对象",
			}
		}
		return encoded, nil
	}
	trimmed := strings.TrimSpace(string(rawBody))
	switch {
	case trimmed == "":
		return nil, &gatewayproto.BuildUpstreamError{
			Code:    gatewayproto.ErrCodeModelMappingRequestInvalid,
			Message: "账号模型映射要求请求体是 JSON 对象",
		}
	case !jsonValid(trimmed):
		return nil, &gatewayproto.BuildUpstreamError{
			Code:    gatewayproto.ErrCodeModelMappingRequestInvalid,
			Message: "账号模型映射要求请求体是有效的 JSON 对象",
		}
	default:
		return nil, &gatewayproto.BuildUpstreamError{
			Code:    gatewayproto.ErrCodeModelMappingRequestInvalid,
			Message: "账号模型映射要求请求体是 JSON 对象",
		}
	}
}

func parseJSONBodyBytes(body []byte) any {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || strings.HasPrefix(trimmed, "event:") {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil
	}
	return value
}

func jsonValid(text string) bool {
	var value any
	return json.Unmarshal([]byte(text), &value) == nil
}

// ---- buffered response semantics ----

// InspectResponse implements ProtocolDriver: extract the JSON semantic
// frames of a buffered upstream response and derive the protocol completion
// evidence / neutral decision.
func (d *Driver) InspectResponse(input gatewayproto.InspectResponseInput) gatewayproto.ResponseInspection {
	family := responseEndpointFamilyFromPath(input.RequestShape.Path)
	value := parseJSONBodyBytes(input.Body)
	frames := ExtractJSONSemanticFrames(value, family)

	inspection := gatewayproto.ResponseInspection{
		EndpointFamily: family,
	}
	for _, frame := range frames {
		switch frame.FrameType {
		case gatewayproto.FrameTypeCompleted:
			if !inspection.ProtocolComplete || inspection.FinishReason == "" {
				inspection.ProtocolComplete = true
				inspection.FinishReason = frame.FinishReason
				inspection.Status = frame.Status
			}
		case gatewayproto.FrameTypeError:
			inspection.Failed = true
			if inspection.ErrorCode == "" {
				inspection.ErrorCode = frame.ErrorCode
			}
			if inspection.ErrorMessage == "" {
				inspection.ErrorMessage = frame.ErrorMessage
			}
		case gatewayproto.FrameTypeUsage:
			if !gatewayproto.HasAnyUsageValue(inspection.Usage) {
				inspection.Usage = frame.Usage
			}
		}
		if frame.VisibleOutput {
			inspection.OutputReceived = true
		}
	}
	// A responses body with an explicit failed/incomplete status carries
	// completion framing but not semantic success.
	if inspection.FinishReason == "failed" || inspection.FinishReason == "incomplete" {
		inspection.Failed = true
	}
	inspection.SemanticSuccess = inspection.ProtocolComplete && !inspection.Failed
	return inspection
}
