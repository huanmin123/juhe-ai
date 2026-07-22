package managementopenaioauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const TokenResponseMaxBytes = 256 * 1024

type TokenInfo struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	ExpiresIn    int64
	ExpiresAt    time.Time
}

type tokenWireResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	IDToken      string          `json:"id_token"`
	TokenType    string          `json:"token_type"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
}

func DecodeTokenResponse(reader io.Reader, now time.Time) (TokenInfo, error) {
	if reader == nil {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("nil OAuth token response body"))
	}
	body, err := io.ReadAll(io.LimitReader(reader, TokenResponseMaxBytes+1))
	if err != nil {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, err)
	}
	if len(body) > TokenResponseMaxBytes {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("OAuth token response exceeds byte limit"))
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var wire tokenWireResponse
	if err := decoder.Decode(&wire); err != nil {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("invalid OAuth token response JSON"))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("trailing OAuth token response JSON"))
	}

	wire.AccessToken = strings.TrimSpace(wire.AccessToken)
	if wire.AccessToken == "" {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("OAuth token response has no access token"))
	}
	expiresIn, err := parseExpiresIn(wire.ExpiresIn)
	if err != nil {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, err)
	}
	if expiresIn > math.MaxInt64/int64(time.Second) {
		return TokenInfo{}, NewError(ErrorCodeUpstreamUnavailable, errors.New("OAuth token expiry overflows duration"))
	}

	return TokenInfo{
		AccessToken:  wire.AccessToken,
		RefreshToken: strings.TrimSpace(wire.RefreshToken),
		IDToken:      strings.TrimSpace(wire.IDToken),
		TokenType:    strings.TrimSpace(wire.TokenType),
		ExpiresIn:    expiresIn,
		ExpiresAt:    now.UTC().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

func parseExpiresIn(raw json.RawMessage) (int64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, errors.New("OAuth token response has no expires_in")
	}
	if strings.HasPrefix(value, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, errors.New("invalid OAuth token expires_in string")
		}
		value = strings.TrimSpace(text)
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) || number <= 0 || math.Trunc(number) != number || number > math.MaxInt64 {
		return 0, errors.New("OAuth token expires_in must be a finite positive integer")
	}
	return int64(number), nil
}
