package gatewaybody

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// Multipart field extraction, mirroring request/multipart-image-metadata.ts.
//
// Approved adaptation: Node uses busboy with limits fields:16,
// fieldSize:max+1, files:5, parts:24; Go uses the standard mime/multipart
// reader wrapped with the same observable limits. Parse errors degrade to
// undefined exactly like the busboy error handler.

const (
	maxMultipartModelBytes          = 200
	maxMultipartResponseFormatBytes = 64
)

// busboy limits shared by both extractors.
const (
	multipartMaxFields = 16
	multipartMaxFiles  = 5
	multipartMaxParts  = 24
)

// ExtractMultipartImageModel mirrors extractGatewayMultipartImageModel.
func ExtractMultipartImageModel(rawBody []byte, contentType string, path string) (string, bool) {
	if !isImageEndpointPath(EndpointPathOf(path)) || !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return "", false
	}
	value, fields := scanMultipartField(rawBody, contentType, "model", maxMultipartModelBytes,
		func(value string) (string, bool) {
			normalized := trimJSSpace(value)
			return normalized, isSafeModelID(normalized)
		})
	if fields != 1 {
		return "", false
	}
	return value, value != ""
}

// ExtractMultipartAudioResponseFormat mirrors extractGatewayMultipartAudioResponseFormat.
func ExtractMultipartAudioResponseFormat(rawBody []byte, contentType string, path string) (string, bool) {
	if !isAudioTranscriptionEndpointPath(EndpointPathOf(path)) || !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return "", false
	}
	value, fields := scanMultipartField(rawBody, contentType, "response_format", maxMultipartResponseFormatBytes,
		func(value string) (string, bool) {
			normalized := strings.ToLower(trimJSSpace(value))
			return normalized, normalized != ""
		})
	if fields != 1 {
		return "", false
	}
	return value, value != ""
}

// scanMultipartField walks the multipart body with the busboy limit
// semantics and returns (accepted value, matching field count). The Node
// handlers resolve only when exactly one matching field passed the
// truncation checks, otherwise undefined.
func scanMultipartField(rawBody []byte, contentType string, fieldName string, maxValueBytes int, normalize func(string) (string, bool)) (string, int) {
	accepted := ""
	matchCount := 0
	err := forEachMultipartPart(rawBody, contentType, maxValueBytes, func(field string, value []byte, valueTruncated bool) {
		if field != fieldName {
			return
		}
		matchCount++
		normalized, ok := normalize(string(value))
		if matchCount == 1 && !valueTruncated && len(normalized) <= maxValueBytes && ok {
			accepted = normalized
		} else {
			accepted = ""
		}
	})
	if err != nil {
		return "", 0
	}
	if matchCount != 1 {
		return "", matchCount
	}
	return accepted, matchCount
}

// forEachMultipartPart iterates the parts with the busboy limit semantics:
// at most multipartMaxParts parts are processed, extra fields are ignored,
// file parts are counted (multipartMaxFiles) and their content is always
// discarded (stream.resume). Malformed bodies surface as errors (busboy
// emits 'error').
func forEachMultipartPart(rawBody []byte, contentType string, maxValueBytes int, onField func(name string, value []byte, valueTruncated bool)) error {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return errNotMultipart
	}
	boundary := params["boundary"]
	if boundary == "" {
		return errNotMultipart
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), boundary)
	partCount := 0
	fieldCount := 0
	fileCount := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		partCount++
		if partCount > multipartMaxParts {
			return nil
		}
		if part.FileName() != "" {
			fileCount++
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		fieldCount++
		if fieldCount > multipartMaxFields {
			continue
		}
		name := part.FormName()
		// busboy fieldSize is maxValueBytes+1; the value is truncated
		// (valueTruncated) when it exceeds that cap, so reading maxValueBytes+2
		// bytes distinguishes truncated from merely over-long values.
		value, readErr := io.ReadAll(io.LimitReader(part, int64(maxValueBytes+2)))
		if readErr != nil {
			return readErr
		}
		onField(name, value, len(value) > maxValueBytes+1)
	}
}

var errNotMultipart = errors.New("content type is not multipart")

// EndpointPathOf mirrors String(req.path || req.originalUrl || ”) query
// stripping: everything before the first '?'.
func EndpointPathOf(path string) string {
	if index := strings.Index(path, "?"); index >= 0 {
		return path[:index]
	}
	return path
}

func isImageEndpointPath(path string) bool {
	path = strings.ToLower(path)
	return path == "/images" || strings.HasPrefix(path, "/images/") ||
		path == "/v1/images" || strings.HasPrefix(path, "/v1/images/")
}

func isAudioTranscriptionEndpointPath(path string) bool {
	path = strings.ToLower(path)
	return path == "/audio/transcriptions" ||
		path == "/audio/translations" ||
		path == "/v1/audio/transcriptions" ||
		path == "/v1/audio/translations"
}

// isSafeModelID mirrors isSafeModelId: non-empty and free of C0 control
// characters and DEL.
func isSafeModelID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}
