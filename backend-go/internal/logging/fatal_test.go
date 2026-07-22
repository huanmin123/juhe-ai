package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
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

func TestWriteFatalReturnsWhenWriterBlocks(t *testing.T) {
	writer := &blockingFatalWriter{started: make(chan struct{}), release: make(chan struct{})}
	defer close(writer.release)
	done := make(chan struct{})
	go func() {
		WriteFatal(writer, errors.New("shutdown failed"))
		close(done)
	}()

	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer was not called")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WriteFatal() did not return while writer was blocked")
	}
}

func TestWriteFatalPreservesQuotesAroundUnquotedCredential(t *testing.T) {
	var output bytes.Buffer
	WriteFatal(&output, errors.New(`unknown command "client_secret=oauth-client-secret-123456" for "juhe-ai"`))

	var record fatalRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if record.Message != `unknown command "client_secret=[REDACTED]" for "juhe-ai"` {
		t.Fatalf("message = %q", record.Message)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

type blockingFatalWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingFatalWriter) Write(data []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(data), nil
}
