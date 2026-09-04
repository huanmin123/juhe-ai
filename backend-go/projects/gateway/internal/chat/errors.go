package chat

// Error taxonomy mirrors the Node chat domain classes and their exact Chinese
// messages. Route mapping lives in writeChatRouteError (routes.go).

// ConflictCode enumerates ChatConflictError codes; the message table is
// byte-identical to chat.repository.ts.
type ConflictCode string

const (
	ConflictMessageInProgress     ConflictCode = "chat_message_in_progress"
	ConflictContextCompacting     ConflictCode = "chat_context_compacting"
	ConflictConversationClearing  ConflictCode = "chat_conversation_clearing"
	ConflictStorageQuotaExceeded  ConflictCode = "chat_storage_quota_exceeded"
	ConflictReplaceConflict       ConflictCode = "chat_replace_conflict"
	ConflictConversationLimit     ConflictCode = "chat_conversation_limit_exceeded"
	ConflictTurnLimitExceeded     ConflictCode = "chat_turn_limit_exceeded"
)

var conflictMessages = map[ConflictCode]string{
	ConflictMessageInProgress:    "当前会话正在生成回答",
	ConflictContextCompacting:    "当前会话正在压缩上下文",
	ConflictConversationClearing: "当前会话正在清空",
	ConflictStorageQuotaExceeded: "聊天容量已达到上限，请先删除部分会话",
	ConflictReplaceConflict:      "最近一轮已变化，请重新确认后再编辑",
	ConflictConversationLimit:    "会话数量已达到上限，请先删除部分会话",
	ConflictTurnLimitExceeded:    "当前会话轮次已达到上限，请新建会话继续提问",
}

// ConflictError maps to Node ChatConflictError → 409 {message, code}.
type ConflictError struct{ Code ConflictCode }

func (e *ConflictError) Error() string {
	if message, ok := conflictMessages[e.Code]; ok {
		return message
	}
	return string(e.Code)
}

// ConversationNotFoundError maps to ChatConversationNotFoundError → 404
// {message: 会话不存在, code: chat_conversation_not_found}.
type ConversationNotFoundError struct{}

func (e *ConversationNotFoundError) Error() string { return "会话不存在" }

// AssistantStorageLimitError maps to ChatAssistantStorageLimitError; the
// finalize path converts the turn to failed before the route surfaces a 500.
type AssistantStorageLimitError struct{}

func (e *AssistantStorageLimitError) Error() string { return "助手回答超过可安全持久化的字节上限" }

// ContextBudgetError maps to ChatContextBudgetError → 422
// {message, code: chat_input_exceeds_context}.
type ContextBudgetError struct{}

func (e *ContextBudgetError) Error() string {
	return "当前输入超过模型上下文窗口，请缩短消息或减少图片后重试"
}

// RequestErrorCode covers ChatRequestError codes (422).
type RequestErrorCode string

const (
	RequestImageNotSupported  RequestErrorCode = "chat_image_not_supported"
	RequestBodyTooLarge       RequestErrorCode = "chat_request_body_too_large"
)

// RequestError maps to Node ChatRequestError → 422 {message, code}.
type RequestError struct {
	Code    RequestErrorCode
	Message string
}

func (e *RequestError) Error() string { return e.Message }

// ModelCapabilityError maps to ChatModelCapabilityError → 422
// {message, code: chat_model_capability_unavailable}.
type ModelCapabilityError struct{ Message string }

func (e *ModelCapabilityError) Error() string { return e.Message }

// ModelContextErrorCode covers ChatModelContextError reasons (422).
type ModelContextErrorCode string

const (
	ModelContextLoadLimit  ModelContextErrorCode = "load_limit"
	ModelContextImagePend  ModelContextErrorCode = "image_pending"
)

// ModelContextError maps to ChatModelContextError → 422
// {message, code: chat_model_context_<reason>}; reason image_pending renders
// the verbatim code chat_model_context_image_pending.
type ModelContextError struct {
	Reason  ModelContextErrorCode
	Message string
}

func (e *ModelContextError) Error() string { return e.Message }

// AssetUploadCode covers ChatAssetUploadError codes; StatusCode carries the
// per-code HTTP status from chat-asset-upload.ts.
type AssetUploadCode string

// AssetUploadError maps to ChatAssetUploadError → status {message, code}.
type AssetUploadError struct {
	Code       AssetUploadCode
	StatusCode int
	Message    string
}

func (e *AssetUploadError) Error() string { return e.Message }

// AssetInputError maps to ChatAssetInputError → 422 {message, code}.
type AssetInputError struct {
	Code    string
	Message string
}

func (e *AssetInputError) Error() string { return e.Message }

// PreparationCanceledError maps to ChatPreparationCanceledError → 499
// {message: 消息准备已取消, code: chat_preparation_canceled}.
type PreparationCanceledError struct{}

func (e *PreparationCanceledError) Error() string { return "消息准备已取消" }

// ContextConflictError maps to ChatContextConflictError (compaction install).
type ContextConflictError struct{ Message string }

func (e *ContextConflictError) Error() string {
	if e.Message == "" {
		return "聊天上下文已变化，当前压缩结果不能安装"
	}
	return e.Message
}

// GatewayUnavailableError maps to ChatGatewayUnavailableError.
type GatewayUnavailableError struct{}

func (e *GatewayUnavailableError) Error() string { return "当前没有可用的内部 Gateway，请稍后重试" }
