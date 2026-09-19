package main

// w11g 覆盖补充：storage preflight 的缺路径/坏路径臂、chat 图片处理边界、
// Gemini 映射 URL 守卫与随机 token 稳定性。

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"

	_ "modernc.org/sqlite"
)

func w11gPreflightBase(t *testing.T) (runtimeConfig, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return runtimeConfig{
		ChatDatabasePath:         filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:      filepath.Join(root, "dataset.sqlite3"),
		UsageCatalogDatabasePath: filepath.Join(root, "usage-catalog.sqlite3"),
		CodexContextShardRoot:    filepath.Join(root, "codex-context"),
		StatsDatabasePath:        filepath.Join(root, "stats.sqlite3"),
		CodexContextShardCount:   1,
		Secret:                   "w11g-preflight-secret",
	}, db
}

// TestW11GPreflightMissingPathArms 已随 2026-09-19 零配置改造移除：路径类
// env 未配置时由 loadRuntimeConfig 按 datadir 固定名表派生，preflight 不再
// 承担缺路径 fail-fast 职责；派生路径直通 preflight 的覆盖见
// storage_bootstrap_test.go TestZeroConfigStoragePreflightWithDerivedPaths。

func TestW11GChatImageProcessingEdges(t *testing.T) {
	processor := newChatImageProcessor()
	// 1x1 PNG：正常处理路径（小图走免缩放编码）。
	small := new(t)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, small); err != nil {
		t.Fatal(err)
	}
	processed, err := processor.ProcessUpload(buffer.Bytes(), "image/png")
	if err != nil {
		t.Fatalf("process upload: %v", err)
	}
	if len(processed.Buffer) == 0 || processed.MimeType != "image/webp" {
		t.Fatalf("processed=%+v", processed.MimeType)
	}
	// 预览：同一输入生成更小的预览图。
	preview, err := processor.CreatePreview(buffer.Bytes())
	if err != nil {
		t.Fatalf("create preview: %v", err)
	}
	if len(preview.Buffer) == 0 || len(preview.Buffer) > chatImagePreviewBytes {
		t.Fatalf("preview bytes=%d", len(preview.Buffer))
	}
	// 坏数据：解码失败归一为处理错误。
	if _, err := processor.ProcessUpload([]byte("w11g-not-an-image"), "image/png"); err == nil {
		t.Fatal("bad data must fail")
	}
	// sqrtOf 边界：非正数与平方数。
	if got := sqrtOf(0); got != 0 {
		t.Fatalf("sqrtOf(0)=%v", got)
	}
	if got := sqrtOf(2); got <= 1.41 || got >= 1.42 {
		t.Fatalf("sqrtOf(2)=%v", got)
	}
	if got := sqrtOf(16); got < 3.9999 || got > 4.0001 {
		t.Fatalf("sqrtOf(16)=%v", got)
	}
}

func new(t *testing.T) image.Image {
	t.Helper()
	return image.NewRGBA(image.Rect(0, 0, 1, 1))
}

func TestW11GGeminiMappedURLGuards(t *testing.T) {
	_ = http.MethodPost
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req := gatewaypreauth.NewGatewayRequest(request)
	// 空/空白上游模型：拒绝。
	if _, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, ""); err == nil {
		t.Fatal("empty upstream model must fail")
	}
	if _, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, "  "); err == nil {
		t.Fatal("blank upstream model must fail")
	}
	// 正常：models/ 前缀剥除后拼 URL。
	url, err := geminiModelMappedUpstreamURL("https://w11g.example.com", req, "models/w11g-model")
	if err != nil || !strings.Contains(url, "w11g-model") {
		t.Fatalf("url=%q err=%v", url, err)
	}
	_ = fmt.Sprint
}
