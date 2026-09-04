package openaicompat

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// TextIndexer ports openai-compatible-vector-stores/text-indexer.ts.

// MaxVectorStoreTextIndexBytes mirrors openAICompatibleVectorStoreTextIndexMaxBytes.
const MaxVectorStoreTextIndexBytes = 2 * 1024 * 1024

const (
	vectorStoreChunkMaxChars     = 2400
	vectorStoreChunkOverlapChars = 400
	vectorStoreMaxChunksPerFile  = 256
)

// IndexingError mirrors OpenAICompatibleVectorStoreIndexingError; the route
// handler renders it with the same gateway payload contract.
type IndexingError struct {
	Message    string
	StatusCode int
	Type       string
	Code       string
}

func (e *IndexingError) Error() string { return e.Message }

func (e *IndexingError) write(w http.ResponseWriter) {
	writeGatewayErrorPayload(w, e.StatusCode, e.Message, e.Type, e.Code)
}

func newIndexingError(message string, statusCode int, errType, code string) *IndexingError {
	return &IndexingError{Message: message, StatusCode: statusCode, Type: errType, Code: code}
}

// IsSupportedVectorStoreTextMediaType mirrors
// isSupportedVectorStoreTextMediaType.
func IsSupportedVectorStoreTextMediaType(mediaType string) bool {
	if mediaType == "" {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		mediaType == "application/typescript" ||
		mediaType == "application/x-sh"
}

// BuildVectorStoreChunks mirrors buildOpenAICompatibleVectorStoreChunks.
// root is the configured openai-compatible files root.
func BuildVectorStoreChunks(root string, file FileRecord) ([]ChunkInput, error) {
	mediaType := ""
	if file.MediaType != nil {
		mediaType = *file.MediaType
	}
	if !IsSupportedVectorStoreTextMediaType(mediaType) {
		return nil, newIndexingError(
			fmt.Sprintf("文件 %s 的媒体类型不受本地向量存储文本索引支持", file.ID),
			400, "invalid_request_error", "openai_compatible_file_mime_unsupported")
	}
	text, err := ReadFileTextForIndexing(root, file)
	if err != nil {
		return nil, err
	}
	normalized := strings.TrimSpace(removeNULCharacters(text))
	if normalized == "" {
		return nil, newIndexingError(
			fmt.Sprintf("文件 %s 不包含可索引文本", file.ID),
			400, "invalid_request_error", "openai_compatible_vector_store_empty_file")
	}
	chunks := chunkTextForVectorStore(normalized)
	if len(chunks) > vectorStoreMaxChunksPerFile {
		return nil, newIndexingError(
			fmt.Sprintf("文件 %s 生成的本地向量存储文本分块过多", file.ID),
			413, "request_too_large", "openai_compatible_vector_store_file_too_large")
	}
	return chunks, nil
}

// removeNULCharacters mirrors text.replace(/\u0000/g, ”).
func removeNULCharacters(text string) string {
	if !strings.Contains(text, "\x00") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != 0 {
			out.WriteByte(text[i])
		}
	}
	return out.String()
}

// ReadFileTextForIndexing mirrors readOpenAICompatibleFileTextForIndexing:
// enforce the byte cap before and during streaming.
func ReadFileTextForIndexing(root string, file FileRecord) (string, error) {
	if file.Bytes > MaxVectorStoreTextIndexBytes {
		return "", newIndexingError(
			fmt.Sprintf("文件 %s 超过本地向量存储文本索引大小限制", file.ID),
			413, "request_too_large", "openai_compatible_vector_store_file_too_large")
	}
	path, err := FileObjectPath(root, file.StorageKey)
	if err != nil {
		return "", err
	}
	handle, openErr := os.Open(path)
	if openErr != nil {
		return "", openErr
	}
	defer handle.Close()
	limited := io.LimitReader(handle, MaxVectorStoreTextIndexBytes+1)
	buffer, readErr := io.ReadAll(limited)
	if readErr != nil {
		return "", readErr
	}
	if int64(len(buffer)) > MaxVectorStoreTextIndexBytes {
		return "", newIndexingError(
			fmt.Sprintf("文件 %s 超过本地向量存储文本索引大小限制", file.ID),
			413, "request_too_large", "openai_compatible_vector_store_file_too_large")
	}
	return string(buffer), nil
}

// chunkTextForVectorStore mirrors chunkTextForVectorStore: fixed-size windows
// with overlap, skipping whitespace-only slices. Node slices UTF-16 code
// units; Go slices runes (identical for BMP text, see package notes).
func chunkTextForVectorStore(text string) []ChunkInput {
	chunks := []ChunkInput{}
	runes := []rune(text)
	start := 0
	for start < len(runes) {
		end := len(runes)
		if start+vectorStoreChunkMaxChars < end {
			end = start + vectorStoreChunkMaxChars
		}
		rawChunk := strings.TrimSpace(string(runes[start:end]))
		if rawChunk != "" {
			chunks = append(chunks, ChunkInput{
				ContentText:      rawChunk,
				ContentPreview:   truncateRunes(collapseWhitespace(rawChunk), 500),
				TokenEstimate:    maxInt(1, int(math.Ceil(float64(len([]rune(rawChunk)))/4))),
				KeywordIndexText: strings.ToLower(collapseWhitespace(rawChunk)),
			})
		}
		if end >= len(runes) {
			break
		}
		start = maxInt(end-vectorStoreChunkOverlapChars, start+1)
	}
	return chunks
}

var whitespaceRun = regexp.MustCompile(`\s+`)

func collapseWhitespace(text string) string {
	return whitespaceRun.ReplaceAllString(text, " ")
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
