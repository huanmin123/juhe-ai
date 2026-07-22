package managementopenaioauth

type AuthURLRequest struct{}

type AuthURLResult struct {
	AuthURL   string `json:"authUrl"`
	SessionID string `json:"sessionId"`
}

// CodeGrant contains only the OAuth fields shared by create and reauthorize
// requests. Account creation fields stay owned by the account module.
type CodeGrant struct {
	SessionID   string `json:"sessionId"`
	CallbackURL string `json:"callbackUrl"`
}

// RefreshGrant intentionally does not implement fmt.Stringer or error. Its value
// must never be copied into logs, operation records, or public errors.
type RefreshGrant struct {
	RefreshToken string `json:"refreshToken"`
}

type ErrorResponse struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
