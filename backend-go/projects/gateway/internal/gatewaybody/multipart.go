package gatewaybody

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
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

// MultipartImageMetadata 是 multipart 图片请求一次扫描得到的模型与计量字段。
// 模型契约与 extractGatewayMultipartImageModel 一致；size/n 供统一媒体模板
// 计量使用（非对话媒体统一计费设计 §7.3）。
type MultipartImageMetadata struct {
	Model    string
	HasModel bool
	Size     string
	N        int
}

// ExtractMultipartImageModel mirrors extractGatewayMultipartImageModel.
func ExtractMultipartImageModel(rawBody []byte, contentType string, path string) (string, bool) {
	metadata := ExtractMultipartImageMetadata(rawBody, contentType, path)
	return metadata.Model, metadata.HasModel
}

// ExtractMultipartImageMetering 提取 multipart 图片请求的生效 size 与 n
// （/images/edits、/images/variations 等）。字段缺失、重复或超限返回零值，
// 由计量层按默认口径处理。
func ExtractMultipartImageMetering(rawBody []byte, contentType string, path string) (size string, n int) {
	metadata := ExtractMultipartImageMetadata(rawBody, contentType, path)
	return metadata.Size, metadata.N
}

// ExtractMultipartImageMetadata 单次遍历 multipart 图片请求，同时提取模型、
// size 与 n。capture 阶段复用该结果可避免终态再次扫描大体积上传体。
func ExtractMultipartImageMetadata(rawBody []byte, contentType string, path string) MultipartImageMetadata {
	var metadata MultipartImageMetadata
	if !isImageEndpointPath(EndpointPathOf(path)) || !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return metadata
	}
	modelCount, sizeCount, nCount := 0, 0, 0
	_ = forEachMultipartPart(rawBody, contentType, maxMultipartModelBytes,
		func(field string, value []byte, valueTruncated bool) {
			switch field {
			case "model":
				modelCount++
				if modelCount == 1 && !valueTruncated {
					normalized := trimJSSpace(string(value))
					if len(normalized) <= maxMultipartModelBytes && isSafeModelID(normalized) {
						metadata.Model = normalized
						metadata.HasModel = true
					}
				}
			case "size":
				sizeCount++
				if sizeCount == 1 && !valueTruncated {
					metadata.Size = strings.ToLower(trimJSSpace(string(value)))
				}
			case "n":
				nCount++
				if nCount == 1 && !valueTruncated {
					parsed, err := strconv.Atoi(trimJSSpace(string(value)))
					if err == nil && parsed > 0 {
						metadata.N = parsed
					}
				}
			}
		})
	if modelCount != 1 {
		metadata.Model = ""
		metadata.HasModel = false
	}
	if sizeCount != 1 {
		metadata.Size = ""
	}
	if nCount != 1 {
		metadata.N = 0
	}
	return metadata
}

// ExtractMultipartFilePart 在已缓冲的 multipart body 中定位指定文件字段，
// 返回内容切片（零拷贝，直接引用 rawBody）与 part Content-Type。用于 STT
// 上传音频时长解析：不复制 64MB 级上传体，也不落盘。字段缺失/重复/结构
// 不完整返回 ok=false。
func ExtractMultipartFilePart(rawBody []byte, contentType, fieldName string) (data []byte, partContentType string, ok bool) {
	data, partContentType, _, ok = ExtractMultipartFilePartInfo(rawBody, contentType, fieldName)
	return data, partContentType, ok
}

// ExtractMultipartFilePartInfo 与 ExtractMultipartFilePart 同源，额外返回
// 文件 part 的 filename（同步转写需从无 Content-Type 的上传推断音频格式）。
func ExtractMultipartFilePartInfo(rawBody []byte, contentType, fieldName string) (data []byte, partContentType, filename string, ok bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return nil, "", "", false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", "", false
	}
	first := []byte("--" + boundary)
	delimiter := []byte("\r\n--" + boundary)
	index := bytes.Index(rawBody, first)
	if index < 0 {
		return nil, "", "", false
	}
	if index > 0 {
		index = bytes.Index(rawBody, delimiter)
		if index < 0 {
			return nil, "", "", false
		}
	}
	partCount := 0
	fileCount := 0
	for {
		partCount++
		if partCount > multipartMaxParts {
			return nil, "", "", false
		}
		headerStart := index + len(delimiter)
		if bytes.HasPrefix(rawBody[index:], first) {
			headerStart = index + len(first)
		}
		if headerStart+2 > len(rawBody) || rawBody[headerStart] != '\r' || rawBody[headerStart+1] != '\n' {
			return nil, "", "", false
		}
		headerStart += 2
		headerEnd := bytes.Index(rawBody[headerStart:], []byte("\r\n\r\n"))
		if headerEnd < 0 {
			return nil, "", "", false
		}
		headers := rawBody[headerStart : headerStart+headerEnd]
		contentStart := headerStart + headerEnd + 4
		next := bytes.Index(rawBody[contentStart:], delimiter)
		if next < 0 {
			return nil, "", "", false
		}
		contentEnd := contentStart + next
		name, partFilename, partType := multipartPartHeaders(headers)
		if partFilename != "" {
			fileCount++
			if fileCount > multipartMaxFiles {
				return nil, "", "", false
			}
			if name == fieldName {
				return rawBody[contentStart:contentEnd], partType, partFilename, true
			}
		}
		index = contentEnd + 2 // 跳过 CRLF，指向下一个 --boundary
		if index+len(first) > len(rawBody) || !bytes.HasPrefix(rawBody[index:], first) {
			return nil, "", "", false
		}
		if bytes.HasPrefix(rawBody[index+len(first):], []byte("--")) {
			return nil, "", "", false
		}
	}
}

// multipartPartHeaders 解析 part 头中的 Content-Disposition 与 Content-Type。
func multipartPartHeaders(headers []byte) (name, filename, contentType string) {
	for _, line := range bytes.Split(headers, []byte("\r\n")) {
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(string(line[:colon])))
		value := strings.TrimSpace(string(line[colon+1:]))
		switch key {
		case "content-disposition":
			if _, params, err := mime.ParseMediaType(value); err == nil {
				name = params["name"]
				filename = params["filename"]
			}
		case "content-type":
			contentType = value
		}
	}
	return name, filename, contentType
}

// ExtractMultipartAudioModel 提取 multipart 语音转写请求的 model 字段。
// STT 的 model 与 file 同为 form-data 字段（对外协议面 §2.1），但请求体
// 不是 JSON，不能走 ExtractJSONBodyMetadata；缺此提取会让调度在入口按
// 「请求缺少 model」404。字段重复、截断或非安全 model ID 时返回 false。
func ExtractMultipartAudioModel(rawBody []byte, contentType string, path string) (string, bool) {
	if !isAudioTranscriptionEndpointPath(EndpointPathOf(path)) || !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return "", false
	}
	value, fields := scanMultipartField(rawBody, contentType, "model", maxMultipartModelBytes,
		func(value string) (string, bool) {
			normalized := trimJSSpace(value)
			return normalized, normalized != "" && isSafeModelID(normalized)
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

// ExtractMultipartAudioLanguage 提取语音转写 multipart 请求的 language 字段
// （可选；用于 verbose_json 回显）。字段重复/截断/空值按缺省处理。
func ExtractMultipartAudioLanguage(rawBody []byte, contentType, path string) (string, bool) {
	if !isAudioTranscriptionEndpointPath(EndpointPathOf(path)) || !strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return "", false
	}
	value, fields := scanMultipartField(rawBody, contentType, "language", maxMultipartResponseFormatBytes,
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

// isAudioEndpointPath 匹配 audio 车道同步端点族（媒体设计 §3：/v1/audio/*
// 路径族优先判定车道）：speech（TTS，JSON 体）与 transcriptions/translations
// （STT，multipart 上传体）。body 限额按此把 audio 车道对齐 image 的 64MB 档。
func isAudioEndpointPath(path string) bool {
	path = strings.ToLower(path)
	if path == "/audio/speech" || path == "/v1/audio/speech" {
		return true
	}
	return isAudioTranscriptionEndpointPath(path)
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
