package playground

// 演练场产物暂存（docs/functions/演练场设计.md §3.8）：图片/语音字节与视频任
// 务引用落 <数据根>/playground-artifacts/{systemAccountId}/ 目录约定，零新表、
// 零新 env（数据根由组合根按 internal/datadir 派生传入 Deps.ArtifactsRoot）。
//
// 边界（§3.8 原文）：**暂存是展示缓存非资产库**——后端不保证 24h 之外的留存，
// 用户留档靠下载；gateway 重启不丢（文件在磁盘）；存储总量 = 每用户最近一批图
// （≤4×16MiB）+ 一条音频 + 一条视频任务引用，受覆盖式替换自然封顶。
//
// 安全面：
//   - id 白名单 sanitizeArtifactID（仅 [A-Za-z0-9._-]，≤128 字符，显式拒绝
//     "."/".."），非法直接 400；
//   - 文件定位只在本用户目录内「扫目录条目按 id 比对」，绝不把 id 拼进路径
//     join，路径穿越与跨用户目录天然不可达（目录名同样过白名单）；
//   - content 端点仅开放媒体文件（图片/音频扩展名），视频任务引用 JSON 与未
//     知扩展名一律 404——视频产物本体走 §3.5 jobId 代理，不经此端点。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 暂存保留期与文件系统参数（§3.8：24h 懒清理；目录/文件最小权限）。
const (
	artifactsRetention  = 24 * time.Hour
	artifactsIDMaxLen   = 128
	artifactsDirPerm    = 0o700
	artifactsFilePerm   = 0o600
	artifactsTokenBytes = 8 // 文件名随机段字节数（8 字节 hex）
)

// artifactContentMaxBytes 是 content 端点的单文件读取上限（暂存写入侧上限的
// 最大值 = 语音 32MiB；超限文件只可能来自带外写入，按产物不存在收敛）。
const artifactContentMaxBytes = speechAudioMaxBytes

// artifactImageMIME / artifactAudioMIME 是暂存文件扩展名 → MIME 词表（图片与
// 语音各一：同 kind 覆盖替换与清单分类按词表判定；语音扩展名来源是
// normalizeSpeechFormat 词表，mp3/wav/ogg/m4a 四值）。artifactMediaMIME 是两
// 者合并视图（content 端点开放面）。
var (
	artifactImageMIME = map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".webp": "image/webp",
	}
	artifactAudioMIME = map[string]string{
		".mp3": "audio/mpeg",
		".wav": "audio/wav",
		".ogg": "audio/ogg",
		".m4a": "audio/mp4",
	}
	artifactMediaMIME = func() map[string]string {
		merged := make(map[string]string, len(artifactImageMIME)+len(artifactAudioMIME))
		for ext, mime := range artifactImageMIME {
			merged[ext] = mime
		}
		for ext, mime := range artifactAudioMIME {
			merged[ext] = mime
		}
		return merged
	}()
	artifactTaskRefExt = ".json"
)

// 暂存错误哨兵（handler 层映射：viewer 非法 → 400 invalid_request；产物不存在
// → 404 artifact_not_found；其余存储面错误 → 500 artifact_store_failed）。
var (
	errArtifactViewerInvalid = errors.New("playground artifact viewer id invalid")
	errArtifactNotFound      = errors.New("playground artifact not found")
)

// decodedImage 是一张已解码生图产物（字节 + 嗅探 MIME），暂存写入的输入形状。
type decodedImage struct {
	Data []byte
	MIME string
}

// videoTaskRef 是视频任务引用（§3.8：视频产物本体不落盘，仅引用随暂存；单条
// 覆盖）。createdAt 为 unix 秒。
type videoTaskRef struct {
	JobID     string `json:"jobId"`
	AccountID string `json:"accountId"`
	ModelID   string `json:"modelId"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
}

// artifactsImageEntry / artifactsSpeechEntry / artifactsVideoEntry 是恢复端点
// GET /artifacts 的清单条目（§3.8 响应形状）；speech/video 无产物时为 null。
type artifactsImageEntry struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"createdAt"`
}

type artifactsSpeechEntry struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MimeType  string `json:"mimeType"`
	CreatedAt int64  `json:"createdAt"`
}

type artifactsVideoEntry struct {
	JobID     string `json:"jobId"`
	AccountID string `json:"accountId"`
	ModelID   string `json:"modelId"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
}

type artifactsResponse struct {
	Images []artifactsImageEntry `json:"images"`
	Speech *artifactsSpeechEntry `json:"speech"`
	Video  *artifactsVideoEntry  `json:"video"`
}

// artifactStore 是暂存区文件存储（每用户一目录；mu 串行化同进程的覆盖替换与
// 懒清理，读路径无锁——展示缓存允许读到半新状态）。root 为空 = 暂存面未接线，
// available() 为 false，产物端点显式降级 503（设计 §1.1 功能不设开关）。
type artifactStore struct {
	root string
	mu   sync.Mutex
}

func newArtifactStore(root string) *artifactStore {
	return &artifactStore{root: strings.TrimSpace(root)}
}

func (s *artifactStore) available() bool { return s.root != "" }

// sanitizeArtifactID 是暂存产物 id 白名单：仅 [A-Za-z0-9._-]、1~128 字符、
// 显式拒绝 "."/".."（字符本身合法但单独出现只有穿越暗示价值）。id 不用于路径
// 拼接（定位是扫目录比对），本函数是入口层的形状防线。
func sanitizeArtifactID(id string) bool {
	if id == "" || len(id) > artifactsIDMaxLen || id == "." || id == ".." {
		return false
	}
	for index := 0; index < len(id); index++ {
		c := id[index]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// userDir 解析当前用户暂存目录（<root>/{systemAccountId}/）：systemAccountId
// 同过白名单——身份来自 RequireSession 后的登录态，此处是目录隔离的防线复核，
// 非法（理论外）返回 errArtifactViewerInvalid。
func (s *artifactStore) userDir(viewerID string) (string, error) {
	if !sanitizeArtifactID(viewerID) {
		return "", errArtifactViewerInvalid
	}
	return filepath.Join(s.root, viewerID), nil
}

// newArtifactID 生成产物 id：{unixNano}-{8 字节随机 hex}（id 即文件名去扩展）。
func newArtifactID() (string, error) {
	token := make([]byte, artifactsTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + hex.EncodeToString(token), nil
}

// artifactContentURL 是暂存产物的前端取流地址（§3.4/§3.6 契约字面量：相对
// playground 基路径，前端经 apiUrl 拼 /__aisys__/api 前缀）。
func artifactContentURL(id string) string {
	return "/playground/artifacts/" + id + "/content"
}

// artifactMIMEByExtension 是暂存扩展名 → MIME 的统一出口（speech 响应
// mimeType、GET artifacts 清单、content 端点 Content-Type 同源）；入参接受裸
// 扩展名（写入方惯例，如 "wav"）与带点形式（ReadDir 的 filepath.Ext，如
// ".wav"）；未知扩展名兜底 octet-stream。
func artifactMIMEByExtension(ext string) string {
	normalized := strings.ToLower(strings.TrimSpace(ext))
	if normalized != "" && !strings.HasPrefix(normalized, ".") {
		normalized = "." + normalized
	}
	if mime, hit := artifactMediaMIME[normalized]; hit {
		return mime
	}
	return "application/octet-stream"
}

// removeKindFiles 删除目录内扩展名命中词表的既有文件（同 kind 覆盖替换的前
// 半步）。单文件删除失败不中断（暂存是缓存，残留文件等 24h 懒清理解收，不把
// 清理失败放大为生成请求失败）。
func removeKindFiles(dir string, extSet map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if _, hit := extSet[ext]; hit {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// replaceImages 同 kind 覆盖写入一批生图产物：先删该用户目录全部旧图，再写新
// 批（n≤4 由上游校验保证），返回响应条目（id + 取流 url）。
func (s *artifactStore) replaceImages(viewerID string, images []decodedImage) ([]imageResultEntry, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, artifactsDirPerm); err != nil {
		return nil, err
	}
	removeKindFiles(dir, artifactImageMIME)
	entries := make([]imageResultEntry, 0, len(images))
	written := make([]string, 0, len(images))
	for _, image := range images {
		id, idErr := newArtifactID()
		if idErr != nil {
			rollbackWritten(dir, written)
			return nil, idErr
		}
		name := id + "." + mimeExtension(image.MIME)
		if err := os.WriteFile(filepath.Join(dir, name), image.Data, artifactsFilePerm); err != nil {
			rollbackWritten(dir, written)
			return nil, err
		}
		written = append(written, name)
		entries = append(entries, imageResultEntry{ID: id, URL: artifactContentURL(id)})
	}
	return entries, nil
}

// replaceSpeech 同 kind 覆盖写入语音产物：删旧音频、写新条（ext 来自请求
// format 词表，缺省 mp3），返回产物 id。
func (s *artifactStore) replaceSpeech(viewerID string, data []byte, ext string) (string, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, artifactsDirPerm); err != nil {
		return "", err
	}
	removeKindFiles(dir, artifactAudioMIME)
	id, idErr := newArtifactID()
	if idErr != nil {
		return "", idErr
	}
	if err := os.WriteFile(filepath.Join(dir, id+"."+ext), data, artifactsFilePerm); err != nil {
		return "", err
	}
	return id, nil
}

// replaceVideoTask 同 kind 覆盖写入视频任务引用（单条 JSON，产物本体不落盘）。
func (s *artifactStore) replaceVideoTask(viewerID string, ref videoTaskRef) error {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, artifactsDirPerm); err != nil {
		return err
	}
	removeKindFiles(dir, map[string]string{artifactTaskRefExt: ""})
	id, idErr := newArtifactID()
	if idErr != nil {
		return idErr
	}
	payload, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+artifactTaskRefExt), payload, artifactsFilePerm)
}

// rollbackWritten 回滚已写入的新产物（写批中途失败时避免残留半批）。
func rollbackWritten(dir string, written []string) {
	for _, name := range written {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// list 读取当前用户暂存清单（§3.8 恢复端点形状）：images 按写入时间升序（批
// 内序 = 生成序）；speech 取最新一条音频；video 取最新任务引用（解析失败视为
// 无引用——展示缓存不因脏文件失败）。目录不存在返回空清单。
func (s *artifactStore) list(viewerID string) (*artifactsResponse, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return nil, err
	}
	out := &artifactsResponse{Images: []artifactsImageEntry{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	type datedFile struct {
		id    string
		ext   string
		name  string
		scope time.Time
	}
	var images []datedFile
	var speech, video *datedFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		item := datedFile{id: id, ext: ext, name: entry.Name(), scope: info.ModTime()}
		if _, isImage := artifactImageMIME[ext]; isImage {
			images = append(images, item)
			continue
		}
		if _, isAudio := artifactAudioMIME[ext]; isAudio {
			if speech == nil || item.scope.After(speech.scope) {
				speech = &item
			}
			continue
		}
		if ext == artifactTaskRefExt {
			if video == nil || item.scope.After(video.scope) {
				video = &item
			}
		}
	}
	sort.Slice(images, func(i, j int) bool {
		if !images[i].scope.Equal(images[j].scope) {
			return images[i].scope.Before(images[j].scope)
		}
		return images[i].name < images[j].name
	})
	for _, image := range images {
		out.Images = append(out.Images, artifactsImageEntry{
			ID:        image.id,
			URL:       artifactContentURL(image.id),
			CreatedAt: image.scope.Unix(),
		})
	}
	if speech != nil {
		out.Speech = &artifactsSpeechEntry{
			ID:        speech.id,
			URL:       artifactContentURL(speech.id),
			MimeType:  artifactAudioMIME[speech.ext],
			CreatedAt: speech.scope.Unix(),
		}
	}
	if video != nil {
		ref, refErr := s.readVideoTaskRef(dir, video.name)
		if refErr == nil {
			out.Video = &artifactsVideoEntry{
				JobID:     ref.JobID,
				AccountID: ref.AccountID,
				ModelID:   ref.ModelID,
				Status:    ref.Status,
				CreatedAt: ref.CreatedAt,
			}
		}
	}
	return out, nil
}

// readVideoTaskRef 读取并解析视频任务引用文件（有界读取，解析失败返回错误由
// 调用方按「无引用」收敛）。
func (s *artifactStore) readVideoTaskRef(dir, name string) (videoTaskRef, error) {
	var ref videoTaskRef
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > artifactContentMaxBytes {
		return ref, errArtifactNotFound
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return ref, err
	}
	if err := json.Unmarshal(payload, &ref); err != nil {
		return ref, err
	}
	return ref, nil
}

// artifactContent 是 content 端点的读取产物（文件名供 ServeContent 兜底嗅探、
// MIME 按扩展名词表预置、ModTime 供条件请求）。
type artifactContent struct {
	Name    string
	MIME    string
	ModTime time.Time
	Data    []byte
}

// openArtifact 在本用户目录内按 id 定位媒体产物（仅开放图片/音频扩展名；视频
// 任务引用 JSON 与未知扩展名一律 errArtifactNotFound——视频内容走 §3.5 jobId
// 代理，不经此端点）。
func (s *artifactStore) openArtifact(viewerID, id string) (*artifactContent, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errArtifactNotFound
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		mime, isMedia := artifactMediaMIME[ext]
		if !isMedia {
			continue
		}
		if strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())) != id {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > artifactContentMaxBytes {
			return nil, errArtifactNotFound
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		return &artifactContent{Name: entry.Name(), MIME: mime, ModTime: info.ModTime(), Data: data}, nil
	}
	return nil, errArtifactNotFound
}

// cleanupExpired 是 24h 懒清理（§3.8）：扫暂存根全部用户目录，删除 mtime 超过
// 保留期的文件与清空后的空用户目录；根目录本身保留。挂全部产物读写点（POST
// images/speech/videos、GET artifacts、GET content）同步执行——每用户文件数受
// 覆盖式替换封顶，扫描代价可忽略；清理失败静默吞掉（缓存 GC 失败不得阻断主
// 生成路径，下一读写点自然重试）。
func (s *artifactStore) cleanupExpired() {
	if !s.available() {
		return
	}
	cutoff := time.Now().Add(-artifactsRetention)
	s.mu.Lock()
	defer s.mu.Unlock()
	rootEntries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, rootEntry := range rootEntries {
		if !rootEntry.IsDir() {
			continue
		}
		dir := filepath.Join(s.root, rootEntry.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
		if remaining, err := os.ReadDir(dir); err == nil && len(remaining) == 0 {
			_ = os.Remove(dir)
		}
	}
}

// --- GET /artifacts 与 GET /artifacts/{id}/content ---

// artifactsHandler 服务 GET /artifacts（§3.8 恢复端点：当前登录用户暂存清单，
// 前端刷新后凭此恢复三页展示与视频轮询）。
func (rt *playgroundRoutes) artifactsHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.artifacts.available() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	rt.artifacts.cleanupExpired()
	snapshot, listErr := rt.artifacts.list(scope.ViewerID)
	if listErr != nil {
		writeHTTPError(w, artifactsError(listErr))
		return
	}
	writeOKPayload(w, snapshot)
}

// artifactContentHandler 服务 GET /artifacts/{id}/content（id 白名单校验非法
// 400；在本用户目录定位，不存在 404；http.ServeContent 透传字节，Content-Type
// 按扩展名词表预置，inline 呈现）。
func (rt *playgroundRoutes) artifactContentHandler(w http.ResponseWriter, r *http.Request) {
	scope, err := rt.deps.ScopeResolver(r)
	if err != nil {
		writeHTTPError(w, unauthorizedError(err))
		return
	}
	if !rt.artifacts.available() {
		writeHTTPError(w, artifactsUnavailable())
		return
	}
	rt.artifacts.cleanupExpired()
	id := strings.TrimSpace(r.PathValue("id"))
	if !sanitizeArtifactID(id) {
		writeHTTPError(w, invalidRequest("artifact id 无效"))
		return
	}
	artifact, openErr := rt.artifacts.openArtifact(scope.ViewerID, id)
	if openErr != nil {
		writeHTTPError(w, artifactsError(openErr))
		return
	}
	w.Header().Set("Content-Type", artifact.MIME)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent 承担条件请求/Range/长度语义；Content-Type 已预置不会被覆盖，
	// 目录结构不经响应暴露。
	http.ServeContent(w, r, artifact.Name, artifact.ModTime, bytes.NewReader(artifact.Data))
}

// artifactsError 把暂存存储错误映射为 HTTP 错误面（哨兵分型：身份非法 → 400，
// 产物不存在 → 404，其余 → 500 保留重试语义）。
func artifactsError(err error) *HTTPError {
	switch {
	case errors.Is(err, errArtifactViewerInvalid):
		return invalidRequest("暂存目录身份无效")
	case errors.Is(err, errArtifactNotFound):
		return &HTTPError{Status: http.StatusNotFound, Code: codeArtifactNotFound, Message: "产物不存在或已过期"}
	default:
		return &HTTPError{Status: http.StatusInternalServerError, Code: codeArtifactStoreFailed, Message: "产物暂存读写失败，请重试"}
	}
}

// artifactsUnavailable 是暂存面未接线（ArtifactsRoot 空）的显式降级。
func artifactsUnavailable() *HTTPError {
	return &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "产物暂存暂不可用，请稍后重试"}
}
