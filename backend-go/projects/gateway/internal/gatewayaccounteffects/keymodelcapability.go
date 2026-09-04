package gatewayaccounteffects

// SourceEndpointMode mirrors sourceMode in key-model-capability.ts: the
// account health check endpoint mode → (endpoint family, stream).
func SourceEndpointMode(mode string) (family string, stream bool, ok bool) {
	switch mode {
	case "chat_json":
		return "chat_completions", false, true
	case "chat_sse":
		return "chat_completions", true, true
	case "responses_json":
		return "responses", false, true
	case "responses_sse":
		return "responses", true, true
	case "messages_json":
		return "messages", false, true
	case "messages_sse":
		return "messages", true, true
	case "generate_content_json":
		return "generate_content", false, true
	case "generate_content_sse":
		return "stream_generate_content", true, true
	case "interactions_json":
		return "interactions", false, true
	case "interactions_sse":
		return "interactions", true, true
	default:
		return "", false, false
	}
}

// EndpointModeForFamily mirrors endpointMode in key-model-capability.ts.
func EndpointModeForFamily(family string, stream bool) string {
	switch family {
	case "chat_completions":
		if stream {
			return "chat_sse"
		}
		return "chat_json"
	case "responses":
		if stream {
			return "responses_sse"
		}
		return "responses_json"
	case "messages":
		if stream {
			return "messages_sse"
		}
		return "messages_json"
	case "generate_content":
		if stream {
			return "generate_content_sse"
		}
		return "generate_content_json"
	case "stream_generate_content":
		return "generate_content_sse"
	case "interactions":
		if stream {
			return "interactions_sse"
		}
		return "interactions_json"
	default:
		return ""
	}
}
