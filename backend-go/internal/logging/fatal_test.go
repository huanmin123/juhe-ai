package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWriteFatalWritesBoundedSanitizedSingleLineJSON(t *testing.T) {
	secretValues := []string{
		"bearer-secret",
		"dXNlcjpiYXNpYy1zZWNyZXQ=",
		"sk-api-secret",
		"sk-proj-1234567890abcdef",
		"token-secret",
		"plain-password-value",
		"json-password-secret",
		`prefix\"escaped-password-secret`,
		"oauth-client-secret-123456",
		"url-user",
		"url-p@ssword",
	}
	message := "startup failed\nwith control \x00\x1f: " +
		"Authorization: Bearer bearer-secret " +
		"Authorization: Basic dXNlcjpiYXNpYy1zZWNyZXQ= " +
		"api_key=sk-api-secret token=token-secret password=plain-password-value " +
		"client_secret=oauth-client-secret-123456 " +
		`credentials={"password":"json-password-secret","pwd":"prefix\"escaped-password-secret"} ` +
		"key sk-proj-1234567890abcdef " +
		"postgres://url-user:url-p@ssword@db.example/juhe " +
		strings.Repeat("界", 5000) + string([]byte{0xff})

	var output bytes.Buffer
	WriteFatal(&output, errors.New(message))

	got := output.Bytes()
	if len(got) > 4096 {
		t.Fatalf("output length = %d, want <= 4096", len(got))
	}
	if !utf8.Valid(got) {
		t.Fatal("output is not valid UTF-8")
	}
	if bytes.Count(got, []byte{'\n'}) != 1 || got[len(got)-1] != '\n' {
		t.Fatalf("output is not exactly one line: %q", got)
	}

	var record struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSuffix(got, []byte{'\n'}), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v; output = %q", err, got)
	}
	if record.Level != "fatal" {
		t.Fatalf("level = %q, want fatal", record.Level)
	}
	if !strings.Contains(record.Message, "[REDACTED]") {
		t.Fatalf("message does not contain redaction marker: %q", record.Message)
	}
	for _, secret := range secretValues {
		if bytes.Contains(got, []byte(secret)) {
			t.Errorf("output leaked secret %q", secret)
		}
	}
}

func TestWriteFatalIgnoresWriterFailure(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("WriteFatal panicked after writer failure: %v", recovered)
		}
	}()

	WriteFatal(failingWriter{}, errors.New("startup failed"))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}
