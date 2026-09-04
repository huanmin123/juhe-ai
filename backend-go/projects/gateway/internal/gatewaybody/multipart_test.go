package gatewaybody

import (
	"bytes"
	"strings"
	"testing"
)

func multipartBody(t *testing.T, parts []string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	for _, part := range parts {
		buf.WriteString("--BOUNDARY\r\n")
		buf.WriteString(part)
		buf.WriteString("\r\n")
	}
	buf.WriteString("--BOUNDARY--\r\n")
	return buf.Bytes(), "multipart/form-data; boundary=BOUNDARY"
}

func formField(name, value string) string {
	return "Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value
}

func filePart(name, filename string) string {
	return "Content-Disposition: form-data; name=\"" + name + "\"; filename=\"" + filename + "\"\r\nContent-Type: image/png\r\n\r\nPNGDATA"
}

func TestExtractMultipartImageModel(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		parts       []string
		want        string
		wantOK      bool
	}{
		{
			name:  "model field on image path",
			path:  "/v1/images/generations",
			parts: []string{formField("model", "gpt-image-1"), filePart("image", "a.png")},
			want:  "gpt-image-1", wantOK: true,
		},
		{
			name:  "model field trimmed",
			path:  "/images",
			parts: []string{formField("model", "  dall-e-3  ")},
			want:  "dall-e-3", wantOK: true,
		},
		{
			name:  "second model field voids the value",
			path:  "/images",
			parts: []string{formField("model", "first"), formField("model", "second")},
		},
		{
			name:  "oversize model rejected",
			path:  "/images",
			parts: []string{formField("model", strings.Repeat("m", 201))},
		},
		{
			name:  "model at the 200 byte boundary accepted",
			path:  "/images",
			parts: []string{formField("model", strings.Repeat("m", 200))},
			want:  strings.Repeat("m", 200), wantOK: true,
		},
		{
			name:  "model with control characters rejected",
			path:  "/images",
			parts: []string{formField("model", "gpt\x01-image")},
		},
		{
			name:  "empty model rejected",
			path:  "/images",
			parts: []string{formField("model", "   ")},
		},
		{
			name:  "model after file part still found",
			path:  "/images",
			parts: []string{filePart("image", "a.png"), formField("model", "gpt-image-1")},
			want:  "gpt-image-1", wantOK: true,
		},
		{
			name:  "wrong path ignored",
			path:  "/v1/chat/completions",
			parts: []string{formField("model", "gpt-image-1")},
		},
		{
			name:        "non multipart content type ignored",
			path:        "/images",
			contentType: "application/json",
			parts:       []string{formField("model", "gpt-image-1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType := tt.contentType
			if contentType == "" {
				contentType = "multipart/form-data; boundary=BOUNDARY"
			}
			raw, boundaryType := multipartBody(t, tt.parts)
			if tt.contentType == "" {
				contentType = boundaryType
			}
			got, ok := ExtractMultipartImageModel(raw, contentType, tt.path)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("ExtractMultipartImageModel() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestExtractMultipartAudioResponseFormat(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		parts  []string
		want   string
		wantOK bool
	}{
		{
			name:  "response format on transcription path",
			path:  "/v1/audio/transcriptions",
			parts: []string{formField("response_format", " Verbose_Json ")},
			want:  "verbose_json", wantOK: true,
		},
		{
			name:  "translation path",
			path:  "/audio/translations",
			parts: []string{formField("response_format", "text")},
			want:  "text", wantOK: true,
		},
		{
			name:  "empty format normalized to undefined",
			path:  "/v1/audio/transcriptions",
			parts: []string{formField("response_format", "   ")},
		},
		{
			name:  "second field voids",
			path:  "/v1/audio/transcriptions",
			parts: []string{formField("response_format", "text"), formField("response_format", "json")},
		},
		{
			name:  "oversize format rejected",
			path:  "/v1/audio/transcriptions",
			parts: []string{formField("response_format", strings.Repeat("f", 65))},
		},
		{
			name:  "format at 64 bytes accepted",
			path:  "/v1/audio/transcriptions",
			parts: []string{formField("response_format", strings.Repeat("f", 64))},
			want:  strings.Repeat("f", 64), wantOK: true,
		},
		{
			name:  "wrong path ignored",
			path:  "/v1/audio",
			parts: []string{formField("response_format", "text")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := multipartBody(t, tt.parts)
			got, ok := ExtractMultipartAudioResponseFormat(raw, "multipart/form-data; boundary=BOUNDARY", tt.path)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("ExtractMultipartAudioResponseFormat() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestMultipartLimitSemantics(t *testing.T) {
	t.Run("fields beyond the busboy cap are ignored", func(t *testing.T) {
		parts := make([]string, 0, 20)
		for i := 0; i < 17; i++ {
			parts = append(parts, formField("padding", "x"))
		}
		parts = append(parts, formField("model", "gpt-image-1"))
		raw, _ := multipartBody(t, parts)
		if _, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images"); ok {
			t.Fatalf("model after 16 fields must be ignored")
		}
	})

	t.Run("model within the field cap is found", func(t *testing.T) {
		parts := []string{formField("model", "gpt-image-1")}
		for i := 0; i < 15; i++ {
			parts = append(parts, formField("padding", "x"))
		}
		raw, _ := multipartBody(t, parts)
		got, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images")
		if !ok || got != "gpt-image-1" {
			t.Fatalf("got = %q, %v", got, ok)
		}
	})

	t.Run("parts beyond the busboy cap stop processing", func(t *testing.T) {
		parts := make([]string, 0, 30)
		for i := 0; i < 24; i++ {
			parts = append(parts, formField("padding", "x"))
		}
		parts = append(parts, formField("model", "gpt-image-1"))
		raw, _ := multipartBody(t, parts)
		if _, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images"); ok {
			t.Fatalf("model after 24 parts must be ignored")
		}
	})

	t.Run("file parts count toward the parts budget", func(t *testing.T) {
		parts := make([]string, 0, 30)
		for i := 0; i < 24; i++ {
			parts = append(parts, filePart("f", "x.png"))
		}
		parts = append(parts, formField("model", "gpt-image-1"))
		raw, _ := multipartBody(t, parts)
		if _, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images"); ok {
			t.Fatalf("model after 24 file parts must be ignored")
		}
	})

	t.Run("malformed body degrades to undefined", func(t *testing.T) {
		raw := []byte("--BOUNDARY\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt-image-1")
		if got, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images"); ok || got != "" {
			t.Fatalf("malformed body must return undefined, got %q, %v", got, ok)
		}
	})

	t.Run("missing boundary degrades to undefined", func(t *testing.T) {
		raw, _ := multipartBody(t, []string{formField("model", "gpt-image-1")})
		if got, ok := ExtractMultipartImageModel(raw, "multipart/form-data", "/images"); ok || got != "" {
			t.Fatalf("missing boundary must return undefined")
		}
	})

	t.Run("binary file content is tolerated", func(t *testing.T) {
		raw := []byte("--BOUNDARY\r\n" +
			"Content-Disposition: form-data; name=\"image\"; filename=\"a.png\"\r\n" +
			"Content-Type: image/png\r\n\r\n" +
			"\x89PNG\r\n\x1a\n\x00\x01" +
			"\r\n--BOUNDARY\r\n" +
			"Content-Disposition: form-data; name=\"model\"\r\n\r\n" +
			"gpt-image-1\r\n--BOUNDARY--\r\n")
		got, ok := ExtractMultipartImageModel(raw, "multipart/form-data; boundary=BOUNDARY", "/images")
		if !ok || got != "gpt-image-1" {
			t.Fatalf("got = %q, %v", got, ok)
		}
	})
}
