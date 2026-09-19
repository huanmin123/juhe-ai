package openaicompatstorage

// aliases.go keeps the original in-package spellings of the symbols that
// moved to openaicompatcore (REFACTOR-0008 阶段 A-1) resolvable inside this
// package, so the domain files migrated in 阶段 B keep their byte-identical
// bodies. Only the symbols this package (production + tests) actually uses
// are forwarded.

import (
	"net/http"
	"net/url"
	"time"

	openaicompatcore "github.com/huanminabc/juhe-ai/backend-go-platform/openaicompatcore"
)

// Shared type vocabulary (aliases keep the Go type identity single-sourced).
type (
	RequestError          = openaicompatcore.RequestError
	BridgeRequestError    = openaicompatcore.BridgeRequestError
	Config                = openaicompatcore.Config
	CodeInterpreterConfig = openaicompatcore.CodeInterpreterConfig
	GatewayScope          = openaicompatcore.GatewayScope
	ScopeResolver         = openaicompatcore.ScopeResolver
)

// Runtime-config constants.
const (
	DefaultMaxFileBytes = openaicompatcore.DefaultMaxFileBytes
	BridgeMaxFileBytes  = openaicompatcore.BridgeMaxFileBytes

	ImageGenerationProviderModel        = openaicompatcore.ImageGenerationProviderModel
	ImageGenerationProviderTimeoutMs    = openaicompatcore.ImageGenerationProviderTimeoutMs
	ImageGenerationProviderMaxBodyBytes = openaicompatcore.ImageGenerationProviderMaxBodyBytes
)

// Error vocabulary forwards (errors.go).
var errUnhandled = openaicompatcore.ErrUnhandled

func newRequestError(message string, statusCode int, errType, code string) *RequestError {
	return openaicompatcore.NewRequestError(message, statusCode, errType, code)
}

func badRequest(message, code string) *RequestError {
	return openaicompatcore.BadRequest(message, code)
}

func notFound(message, code string) *RequestError {
	return openaicompatcore.NotFound(message, code)
}

func bridgeError(message, code string, statusCode int, errType string) *BridgeRequestError {
	return openaicompatcore.BridgeError(message, code, statusCode, errType)
}

func writeGatewayErrorPayload(w http.ResponseWriter, status int, message, errType, code string) {
	openaicompatcore.WriteGatewayErrorPayload(w, status, message, errType, code)
}

func writeUnhandledError(w http.ResponseWriter) {
	openaicompatcore.WriteUnhandledError(w)
}

// Parsing primitive forwards (httputil.go).
func parseJSNumber(text string) (float64, bool) {
	return openaicompatcore.ParseJSNumber(text)
}

func queryStringParam(query url.Values, name string) *string {
	return openaicompatcore.QueryStringParam(query, name)
}

func queryIntegerParam(query url.Values, name string) *int {
	return openaicompatcore.QueryIntegerParam(query, name)
}

func queryIntegerValue(value any) *int {
	return openaicompatcore.QueryIntegerValue(value)
}

func queryNumberValue(value any) *float64 {
	return openaicompatcore.QueryNumberValue(value)
}

func stringValue(value any) *string {
	return openaicompatcore.StringValue(value)
}

func objectValue(value any) map[string]any {
	return openaicompatcore.ObjectValue(value)
}

func readJSONObjectBody(r *http.Request) (map[string]any, error) {
	return openaicompatcore.ReadJSONObjectBody(r)
}

// Time primitive forwards (timeutil.go).
func openAITimestamp(value string) (int64, error) {
	return openaicompatcore.OpenAITimestamp(value)
}

func isoMillis(t time.Time) string {
	return openaicompatcore.IsoMillis(t)
}

func expiresAtFromDays(days *int, now time.Time) *string {
	return openaicompatcore.ExpiresAtFromDays(days, now)
}

func randomHex(characters int) string {
	return openaicompatcore.RandomHex(characters)
}

func newOpenAICompatibleFileID(now time.Time) string {
	return openaicompatcore.NewOpenAICompatibleFileID(now)
}

func newOpenAICompatibleVectorStoreID(now time.Time) string {
	return openaicompatcore.NewOpenAICompatibleVectorStoreID(now)
}

func newVectorStoreChunkID() string {
	return openaicompatcore.NewVectorStoreChunkID()
}
