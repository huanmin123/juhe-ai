package chat

import "encoding/hex"

// hexEncode renders bytes as lowercase hex (hex.EncodeToString alias used
// across the generation files).
func hexEncode(data []byte) string { return hex.EncodeToString(data) }
