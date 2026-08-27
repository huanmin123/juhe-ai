package modelcheckprobe

import (
	"errors"
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

// O200kTokenizer is the Gateway-native implementation of Node's pinned
// js-tiktoken o200k_base encoder. The vocabulary is embedded by the Go
// dependency, so no Node process, IPC, or runtime download is involved.
type O200kTokenizer struct {
	codec tokenizer.Codec
}

func NewO200kTokenizer() (*O200kTokenizer, error) {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return nil, err
	}
	return &O200kTokenizer{codec: codec}, nil
}

func (t *O200kTokenizer) Version() string { return "js-tiktoken@1.0.21:o200k_base" }

func (t *O200kTokenizer) Count(value string) (int, error) {
	if t == nil || t.codec == nil {
		return 0, errors.New("o200k tokenizer is not initialized")
	}
	if strings.TrimSpace(value) == "" {
		// The encoder correctly returns zero for an empty string; retain the
		// explicit branch so a nil/invalid codec cannot be mistaken for that.
		return 0, nil
	}
	return t.codec.Count(value)
}
