package systemteams

import (
	crand "crypto/rand"
	"encoding/hex"
)

func randomHex() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}
