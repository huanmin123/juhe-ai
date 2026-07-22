package gateway

// OpenAIOperation refines EndpointFamily when routing decisions must
// distinguish a primary generation request from a related sub-operation.
type OpenAIOperation string

const (
	OpenAIOperationUnknown               OpenAIOperation = ""
	OpenAIOperationChatCompletionsCreate OpenAIOperation = "chat_completions.create"
	OpenAIOperationResponsesCreate       OpenAIOperation = "responses.create"
	OpenAIOperationResponsesCompact      OpenAIOperation = "responses.compact"
)

// OpenAIOperationFromPath classifies exact OpenAI generation operations using
// the same normalization as the shared endpoint-family classifier.
func OpenAIOperationFromPath(pathAndQuery string) OpenAIOperation {
	switch normalizedPath(pathAndQuery, "/v1") {
	case "/chat/completions", "/chat/completions/":
		return OpenAIOperationChatCompletionsCreate
	case "/responses", "/responses/":
		return OpenAIOperationResponsesCreate
	case "/responses/compact", "/responses/compact/":
		return OpenAIOperationResponsesCompact
	default:
		return OpenAIOperationUnknown
	}
}
