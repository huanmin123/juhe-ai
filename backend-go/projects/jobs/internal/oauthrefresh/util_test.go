package oauthrefresh

import "encoding/base64"

func decodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(value)
}

func encodeBase64URL(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
