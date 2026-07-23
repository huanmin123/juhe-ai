package gateway

import "strings"

type ReplayClass string

const (
	ReplaySafeRead         ReplayClass = "safe_read"
	ReplayInference        ReplayClass = "stateless_inference"
	ReplayResourceMutation ReplayClass = "resource_mutation"
	ReplayUnknown          ReplayClass = "unknown"
)

type ReplayPolicy struct {
	Allowed  bool
	Class    ReplayClass
	Reason   string
	Protocol ProtocolCode
	Family   EndpointFamily
}

// ClassifyReplay is deliberately exact. It prevents a profile fallback from
// turning an unknown POST into a replayable request and treats resource
// mutations as non-replayable even when their transport looks identical.
func ClassifyReplay(request RequestShape, profile *Profile) ReplayPolicy {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	protocol, _ := NativeProtocolForRequest(request)
	if protocol == "" && profile != nil {
		protocol = ProtocolCode(strings.ToLower(strings.TrimSpace(profile.Code)))
	}
	family := EndpointFamilyFromPath(protocol, request.Path)
	path := normalizedPathForReplay(request.Path, protocol)
	if (method == "GET" || method == "HEAD") && family == EndpointModels {
		return ReplayPolicy{Allowed: true, Class: ReplaySafeRead, Reason: "safe_read", Protocol: protocol, Family: family}
	}
	if method != "POST" {
		return ReplayPolicy{Class: ReplayUnknown, Reason: "method_not_replayable", Protocol: protocol, Family: family}
	}
	if isReplayablePost(protocol, path, request) {
		return ReplayPolicy{Allowed: true, Class: ReplayInference, Reason: "stateless_inference", Protocol: protocol, Family: family}
	}
	if family != EndpointUnknown {
		return ReplayPolicy{Class: ReplayResourceMutation, Reason: "resource_or_protocol_action", Protocol: protocol, Family: family}
	}
	return ReplayPolicy{Class: ReplayUnknown, Reason: "unknown_post", Protocol: protocol, Family: family}
}

func isReplayablePost(protocol ProtocolCode, path string, request RequestShape) bool {
	switch protocol {
	case ProtocolOpenAI:
		switch path {
		case "/embeddings", "/images/generations", "/images/edits":
			return true
		case "/chat/completions":
			return request.StoreRequest.State == StoreAbsent || request.StoreRequest.State == StoreNull || request.StoreRequest.State == StoreExplicitFalse
		case "/responses":
			return request.StoreRequest.State == StoreExplicitFalse
		}
	case ProtocolAnthropic:
		return path == "/messages" || path == "/messages/count_tokens"
	case ProtocolGemini:
		for _, suffix := range []string{":generatecontent", ":streamgeneratecontent", ":counttokens", ":embedcontent"} {
			if strings.HasPrefix(path, "/models/") && strings.HasSuffix(path, suffix) && strings.Count(path, "/") == 2 {
				return true
			}
		}
	}
	return false
}

func normalizedPathForReplay(path string, protocol ProtocolCode) string {
	path = strings.ToLower(strings.TrimSpace(strings.SplitN(path, "?", 2)[0]))
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	version := "/v1"
	if protocol == ProtocolGemini {
		version = "/v1beta"
	}
	if path == version {
		return "/"
	}
	if strings.HasPrefix(path, version+"/") {
		return strings.TrimPrefix(path, version)
	}
	return path
}
