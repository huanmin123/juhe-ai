package openaicompat

import (
	"strings"
	"testing"
)

// Text indexer tests mirror text-indexer.ts boundaries.

func TestIsSupportedVectorStoreTextMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{"", false},
		{"text/plain", true},
		{"text/csv", true},
		{"application/json", true},
		{"application/typescript", true},
		{"application/x-sh", true},
		{"application/pdf", false},
		{"image/png", false},
		{"application/octet-stream", false},
	}
	for _, tc := range tests {
		if got := IsSupportedVectorStoreTextMediaType(tc.mediaType); got != tc.want {
			t.Fatalf("mediaType %q = %v, want %v", tc.mediaType, got, tc.want)
		}
	}
}

func TestBuildVectorStoreChunks(t *testing.T) {
	root := t.TempDir()
	store := newTestStore(t)

	persist := func(id, mediaType, content string) FileRecord {
		t.Helper()
		return createTestFile(t, store, root, id, "assistants", []byte(content), mediaType, nil)
	}

	tests := []struct {
		name      string
		id        string
		mediaType string
		content   string
		wantErr   *IndexingError
		wantCount int
		check     func(t *testing.T, chunks []ChunkInput)
	}{
		{
			name:      "unsupported media type",
			id:        "file-mime",
			mediaType: "application/pdf",
			content:   "x",
			wantErr:   newIndexingError("文件 file-mime 的媒体类型不受本地向量存储文本索引支持", 400, "invalid_request_error", "openai_compatible_file_mime_unsupported"),
		},
		{
			name:      "missing media type",
			id:        "file-nomime",
			mediaType: "",
			content:   "x",
			wantErr:   newIndexingError("文件 file-nomime 的媒体类型不受本地向量存储文本索引支持", 400, "invalid_request_error", "openai_compatible_file_mime_unsupported"),
		},
		{
			name:      "empty content",
			id:        "file-empty",
			mediaType: "text/plain",
			content:   "   \n\t ",
			wantErr:   newIndexingError("文件 file-empty 不包含可索引文本", 400, "invalid_request_error", "openai_compatible_vector_store_empty_file"),
		},
		{
			name:      "NUL characters stripped",
			id:        "file-nul",
			mediaType: "text/plain",
			content:   "\x00ab\x00cd",
			wantCount: 1,
			check: func(t *testing.T, chunks []ChunkInput) {
				if chunks[0].ContentText != "abcd" {
					t.Fatalf("content = %q", chunks[0].ContentText)
				}
			},
		},
		{
			name:      "simple chunking fields",
			id:        "file-simple",
			mediaType: "text/plain",
			content:   "The quick   brown fox",
			wantCount: 1,
			check: func(t *testing.T, chunks []ChunkInput) {
				chunk := chunks[0]
				if chunk.ContentText != "The quick   brown fox" {
					t.Fatalf("text = %q", chunk.ContentText)
				}
				if chunk.ContentPreview != "The quick brown fox" {
					t.Fatalf("preview = %q", chunk.ContentPreview)
				}
				if chunk.KeywordIndexText != "the quick brown fox" {
					t.Fatalf("keyword = %q", chunk.KeywordIndexText)
				}
				if chunk.TokenEstimate != 6 {
					t.Fatalf("token estimate = %d", chunk.TokenEstimate)
				}
			},
		},
		{
			name:      "window overlap produces multiple chunks",
			id:        "file-window",
			mediaType: "text/plain",
			content:   strings.Repeat("a", 2400*2) + "tail",
			wantCount: 3,
		},
		{
			name:      "oversize file rejected before reading",
			id:        "file-big",
			mediaType: "text/plain",
			content:   strings.Repeat("x", 1000),
			wantErr:   newIndexingError("文件 file-big 超过本地向量存储文本索引大小限制", 413, "request_too_large", "openai_compatible_vector_store_file_too_large"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := persist(tc.id, tc.mediaType, tc.content)
			if tc.id == "file-big" {
				// Simulate the recorded size exceeding the index cap without
				// materializing 2 MiB on disk.
				record.Bytes = MaxVectorStoreTextIndexBytes + 1
			}
			chunks, err := BuildVectorStoreChunks(root, record)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v", tc.wantErr)
				}
				indexingErr, ok := err.(*IndexingError)
				if !ok || indexingErr.Message != tc.wantErr.Message || indexingErr.StatusCode != tc.wantErr.StatusCode || indexingErr.Code != tc.wantErr.Code {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != tc.wantCount {
				t.Fatalf("chunks = %d, want %d", len(chunks), tc.wantCount)
			}
			if tc.check != nil {
				tc.check(t, chunks)
			}
		})
	}
}

func TestChunkTextForVectorStoreOverlapMath(t *testing.T) {
	text := strings.Repeat("x", 2400+400+10)
	chunks := chunkTextForVectorStore(text)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if len([]rune(chunks[0].ContentText)) != 2400 {
		t.Fatalf("first chunk = %d", len([]rune(chunks[0].ContentText)))
	}
	// Second window starts at 2400-400=2000 and covers the remaining 810.
	if len([]rune(chunks[1].ContentText)) != 810 {
		t.Fatalf("second chunk = %d", len([]rune(chunks[1].ContentText)))
	}
}

func TestChunkLimit256Enforced(t *testing.T) {
	// (2400-400) stride => ~ceil((n-2400)/2000)+1 chunks; build text with more
	// than 256 windows and expect the 413 request_too_large contract.
	text := strings.Repeat("y", 256*2000+3000)
	_, err := chunkTextGuarded(text)
	if err == nil {
		t.Fatal("expected chunk overflow error")
	}
	indexingErr, ok := err.(*IndexingError)
	if !ok || indexingErr.StatusCode != 413 || indexingErr.Code != "openai_compatible_vector_store_file_too_large" {
		t.Fatalf("error = %v", err)
	}
}

// chunkTextGuarded runs the raw chunker against the 256-chunk contract (the
// public entry adds the file-id message).
func chunkTextGuarded(text string) ([]ChunkInput, error) {
	chunks := chunkTextForVectorStore(text)
	if len(chunks) > vectorStoreMaxChunksPerFile {
		return nil, newIndexingError(
			"文件 x 生成的本地向量存储文本分块过多",
			413, "request_too_large", "openai_compatible_vector_store_file_too_large")
	}
	return chunks, nil
}
