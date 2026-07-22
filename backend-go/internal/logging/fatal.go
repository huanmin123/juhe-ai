package logging

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	maxFatalOutputBytes = 4096
	fatalWriteTimeout   = 250 * time.Millisecond
)

var (
	urlUserInfoPattern   = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s]+@`)
	authorizationPattern = regexp.MustCompile(`(?i)\b(Authorization[ \t]*:[ \t]*(?:Bearer|Basic))[ \t]+[^\s,;"']+`)
	jsonCredential       = regexp.MustCompile(`(?i)(["'][a-z0-9_-]*(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|token|password|passwd|pwd|secret)["'][ \t]*:[ \t]*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;&}"']+)`)
	credentialPattern    = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|token|password|passwd|pwd|secret))([ \t]*[:=][ \t]*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;&"']+)`)
	credentialFlag       = regexp.MustCompile(`(?i)(--(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|token|password|passwd|pwd|secret)[ \t]+)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;&"']+)`)
	openAIAPIKeyPattern  = regexp.MustCompile(`(?i)\bsk-(?:proj-|svcacct-)?[a-z0-9_-]{12,}\b`)
)

type fatalRecord struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// WriteFatal writes one bounded diagnostic record and deliberately ignores output failures.
func WriteFatal(writer io.Writer, err error) {
	if writer == nil {
		return
	}

	message := ""
	if err != nil {
		message = sanitizeFatalMessage(err.Error())
	}
	encoded := marshalBoundedFatal(message)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		_, _ = writer.Write(encoded)
	}()
	timer := time.NewTimer(fatalWriteTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func sanitizeFatalMessage(message string) string {
	message = strings.ToValidUTF8(message, "�")
	message = urlUserInfoPattern.ReplaceAllString(message, `${1}[REDACTED]@`)
	message = authorizationPattern.ReplaceAllString(message, `${1} [REDACTED]`)
	message = jsonCredential.ReplaceAllString(message, `${1}"[REDACTED]"`)
	message = credentialPattern.ReplaceAllString(message, `${1}${2}[REDACTED]`)
	message = credentialFlag.ReplaceAllString(message, `${1}[REDACTED]`)
	return openAIAPIKeyPattern.ReplaceAllString(message, `[REDACTED]`)
}

func marshalBoundedFatal(message string) []byte {
	runes := []rune(message)
	low, high := 0, len(runes)
	var encoded []byte
	for low <= high {
		middle := low + (high-low)/2
		candidate, _ := json.Marshal(fatalRecord{Level: "fatal", Message: string(runes[:middle])})
		if len(candidate)+1 <= maxFatalOutputBytes {
			encoded = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return append(encoded, '\n')
}
