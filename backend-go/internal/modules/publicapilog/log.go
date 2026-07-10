package publicapilog

import (
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type SourceContext struct {
	SourceRefID string
	SourceName  string
	TokenID     string
	TokenName   string
	TokenPrefix string
	IsTestToken bool
}

type BuildInput struct {
	ID               string
	TraceID          string
	Source           *SourceContext
	Method           string
	Path             string
	QueryString      string
	ClientIP         string
	UserAgent        string
	StatusCode       int
	DurationMs       int64
	RequestSnapshot  Snapshot
	ResponseSnapshot Snapshot
	ErrorCode        string
	ErrorMessage     string
	StartedAt        time.Time
	EndedAt          time.Time
	Closed           bool
}

func BuildPublicAPILogInput(input BuildInput) port.PublicAPILogInput {
	statusCode := input.StatusCode
	if input.Closed {
		statusCode = 499
	}
	durationMs := input.DurationMs
	endedAt := input.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	log := port.PublicAPILogInput{
		ID:                    input.ID,
		TraceID:               input.TraceID,
		Method:                strings.ToUpper(input.Method),
		Path:                  input.Path,
		QueryString:           input.QueryString,
		ClientIP:              input.ClientIP,
		UserAgent:             input.UserAgent,
		StatusCode:            &statusCode,
		Success:               !input.Closed && statusCode >= 200 && statusCode < 400,
		DurationMs:            &durationMs,
		RequestSizeBytes:      input.RequestSnapshot.SizeBytes,
		ResponseSizeBytes:     input.ResponseSnapshot.SizeBytes,
		RequestCaptureStatus:  input.RequestSnapshot.Status,
		ResponseCaptureStatus: input.ResponseSnapshot.Status,
		RequestData:           input.RequestSnapshot.Data,
		ResponseData:          input.ResponseSnapshot.Data,
		ErrorCode:             input.ErrorCode,
		ErrorMessage:          input.ErrorMessage,
		StartedAt:             input.StartedAt.UTC(),
		EndedAt:               endedAt.UTC(),
		CreatedAt:             endedAt.UTC(),
	}
	if input.Closed {
		log.ErrorCode = "public_api_client_closed"
		log.ErrorMessage = "客户端连接提前关闭"
	}
	if input.Source != nil {
		log.SourceRefID = input.Source.SourceRefID
		log.SourceName = input.Source.SourceName
		log.TokenID = input.Source.TokenID
		log.TokenName = input.Source.TokenName
		log.TokenPrefix = input.Source.TokenPrefix
		log.IsTestToken = input.Source.IsTestToken
	}
	return log
}

func ErrorInfoFromResponse(payload any, statusCode int) (string, string) {
	if statusCode < 400 {
		return "", ""
	}
	if record, ok := payload.(map[string]any); ok {
		nested := map[string]any(nil)
		if nestedValue, ok := record["error"].(map[string]any); ok {
			nested = nestedValue
		}
		return firstString(record["code"], record["type"], nested["code"], nested["type"]),
			firstString(record["message"], nested["message"], record["error"])
	}
	if text, ok := payload.(string); ok {
		return "", truncateRunes(strings.TrimSpace(text), 1000)
	}
	if statusCode >= 500 {
		return "", "服务器内部错误"
	}
	return "", "请求失败：HTTP " + intString(statusCode)
}

func firstString(values ...any) string {
	for _, value := range values {
		text, ok := value.(string)
		if ok && strings.TrimSpace(text) != "" {
			return truncateRunes(strings.TrimSpace(text), 1000)
		}
	}
	return ""
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}
