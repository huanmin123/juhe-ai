package openaicompat

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxTextIndexBytes is re-exported next to the storage helpers for
// convenience of embedders (text-indexer keeps its own constant too).
const MaxTextIndexBytes = 2 * 1024 * 1024

// StorageKeyEscapeError mirrors the openAICompatibleFileObjectPath throw
// "OpenAI compatible file storage key escaped storage root".
var StorageKeyEscapeError = errors.New("OpenAI compatible file storage key escaped storage root")

var storageUnsafeCharacters = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// NewFileID returns a fresh file id using the default generator
// (file-<base36 ms>-<20 hex>).
func NewFileID(now time.Time) string { return newOpenAICompatibleFileID(now) }

// StorageKeyForFile mirrors storageKeyForOpenAICompatibleFile:
// files/<8-char shard>/<sanitized id>. The shard comes from the raw sanitized
// id (before its own "file" fallback), so an empty id lands in "default".
func StorageKeyForFile(fileID string) string {
	safeID := safeStorageSegment(fileID)
	shard := safeID
	if len([]rune(shard)) > 8 {
		shard = string([]rune(shard)[:8])
	}
	if shard == "" {
		shard = "default"
	}
	return "files/" + shard + "/" + safeID
}

// safeStorageSegment mirrors safeStorageSegment: replace anything outside
// [A-Za-z0-9_.-] with '_', clamp to 160 characters, fall back to "file".
func safeStorageSegment(value string) string {
	replaced := storageUnsafeCharacters.ReplaceAllString(value, "_")
	runes := []rune(replaced)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	segment := string(runes)
	if segment == "" {
		return "file"
	}
	return segment
}

// isRootAnchoredKey reports keys that start at a filesystem root: absolute
// paths, volume-less rooted paths (backslash-x, forward-slash-x) or drive
// paths (C:x).
func isRootAnchoredKey(storageKey string) bool {
	if filepath.IsAbs(storageKey) || strings.HasPrefix(storageKey, "/") || strings.HasPrefix(storageKey, "\\") {
		return true
	}
	return len(storageKey) >= 2 && storageKey[1] == ':'
}

// FileObjectPath mirrors openAICompatibleFileObjectPath: resolve the storage
// key under root and refuse anything that escapes the root (Node checks the
// relative path for "..", an empty result or a Windows drive prefix).
func FileObjectPath(root, storageKey string) (string, error) {
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootPath = filepath.Clean(rootPath)
	var target string
	if isRootAnchoredKey(storageKey) {
		// Node resolve(root, "/x") jumps to the drive root of root and the
		// subsequent relative check rejects it; mirror that rejection.
		target = filepath.Clean(storageKey)
	} else {
		target = filepath.Clean(filepath.Join(rootPath, storageKey))
	}
	relativePath, err := filepath.Rel(rootPath, target)
	if err != nil {
		return "", StorageKeyEscapeError
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) ||
		relativePath == "." || relativePath == "" || filepath.IsAbs(relativePath) {
		return "", StorageKeyEscapeError
	}
	return target, nil
}

// EnsureFileObjectParent mirrors ensureOpenAICompatibleFileObjectParent.
func EnsureFileObjectParent(root, storageKey string) (string, error) {
	filePath, err := FileObjectPath(root, storageKey)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return "", err
	}
	return filePath, nil
}

// RemoveFileObject mirrors removeOpenAICompatibleFileObject: missing files
// are not an error.
func RemoveFileObject(root, storageKey string) error {
	filePath, err := FileObjectPath(root, storageKey)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Remove(filePath)
}

// MediaTypeFromFilename mirrors mediaTypeFromFilename (exact suffix table and
// precedence order).
func MediaTypeFromFilename(filename string) string {
	lower := strings.ToLower(strings.TrimSpace(filename))
	if lower == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".csv"):
		return "text/csv"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".c"):
		return "text/x-c"
	case strings.HasSuffix(lower, ".cpp"), strings.HasSuffix(lower, ".cc"), strings.HasSuffix(lower, ".cxx"):
		return "text/x-c++"
	case strings.HasSuffix(lower, ".cs"):
		return "text/x-csharp"
	case strings.HasSuffix(lower, ".css"):
		return "text/css"
	case strings.HasSuffix(lower, ".go"):
		return "text/x-golang"
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "text/html"
	case strings.HasSuffix(lower, ".java"):
		return "text/x-java"
	case strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".mjs"), strings.HasSuffix(lower, ".cjs"):
		return "text/javascript"
	case strings.HasSuffix(lower, ".php"):
		return "text/x-php"
	case strings.HasSuffix(lower, ".py"):
		return "text/x-python"
	case strings.HasSuffix(lower, ".rb"):
		return "text/x-ruby"
	case strings.HasSuffix(lower, ".tex"):
		return "text/x-tex"
	case strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".tsx"):
		return "application/typescript"
	case strings.HasSuffix(lower, ".sh"):
		return "application/x-sh"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}

// NormalizeFileMediaType mirrors normalizeOpenAICompatibleFileMediaType.
func NormalizeFileMediaType(value, filename string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	if mediaType != "" && mediaType != "application/octet-stream" {
		return mediaType
	}
	return MediaTypeFromFilename(filename)
}

// NormalizedUploadFilename mirrors normalizedUploadFilename: last segment
// across / and \ separators, trimmed, defaulting to "upload".
func NormalizedUploadFilename(value string) string {
	segments := strings.Split(value, "/")
	last := segments[len(segments)-1]
	if index := strings.LastIndex(last, "\\"); index >= 0 {
		last = last[index+1:]
	}
	filename := strings.TrimSpace(last)
	if filename == "" {
		return "upload"
	}
	return filename
}
