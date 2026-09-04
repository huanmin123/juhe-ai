package gatewaydispatch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// decodeJSONObject parses a JSON object payload into a map; ok=false when
// the payload is not a JSON object (mirrors the isPlainObject guard).
func decodeJSONObject(raw []byte) (map[string]any, bool) {
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}
	object, ok := parsed.(map[string]any)
	return object, ok
}

// isPlainObjectValue mirrors the Node isPlainObject guard.
func isPlainObjectValue(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// uuid4String mirrors Node randomUUID() (v4, lowercase, dashed).
func uuid4String() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// trimString mirrors the Node `.trim()`-based normalization used throughout
// the dispatch pipeline (empty string stays empty).
func trimString(value string) string {
	return strings.TrimSpace(value)
}

// sha256HexBytes mirrors Node createHash('sha256')...digest('hex').
func sha256HexBytes(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// jsonCloneValue deep-clones through a JSON round trip (mirrors structured
// clone usage in the codex normalizer).
func jsonCloneValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil
	}
	return cloned
}
