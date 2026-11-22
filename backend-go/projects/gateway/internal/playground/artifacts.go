package playground

// 演练场产物暂存（docs/functions/演练场设计.md §3.8，v1.2 双保留期）：语音
// 字节、视频任务引用与图像工作台的结果图/参考图存 juhe_business.playground_
// artifacts（PG bytea，多节点无需共享盘；job_id 列关联图像产物与任务）。零新
// env；组合根注入 *sql.DB。
//
// 保留期（§3.8 v1.2 裁决）：
//   - image_result / image_reference：随所属任务 7 天（expires_at 记在任务
//     行），字节删除由 jobs 项目定时任务执行（记录行永不删）；
//   - 孤儿参考图（image_reference 且 job_id=''）：24h 懒清理；
//   - speech / video_task：24h 覆盖式（v1.0 口径不变）。
//
// 边界（§3.8 原文）：**暂存是展示缓存非资产库**——图像字节不保证 beyond 7 天，
// 用户留档靠下载；语音/视频受覆盖式替换自然封顶。
//
// 安全面：
//   - id 白名单 sanitizeArtifactID（仅 [A-Za-z0-9._-]，≤128 字符，显式拒绝
//     "."/".."），非法直接 400；
//   - 查询按 system_account_id + id 定位，跨用户不可达；
//   - content 端点仅开放媒体产物（kind=image_result/image_reference/speech），
//     视频任务引用 JSON 一律 404——视频产物本体走 §3.5 jobId 代理，不经此端
//     点；v1.0 退役的 kind='image' 行不再开放（存量由 jobs 清理任务顺带删除）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
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

// artifactImageMIME / artifactAudioMIME 是暂存文件扩展名 → MIME 词表（图像与
// 语音各一：文件替身的 kind 归类与语音覆盖替换按词表判定；语音扩展名来源是
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
	errArtifactViewerInvalid    = errors.New("playground artifact viewer id invalid")
	errArtifactNotFound         = errors.New("playground artifact not found")
	errArtifactStoreUnavailable = errors.New("playground artifact store unavailable")
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

// artifactsSpeechEntry / artifactsVideoEntry 是恢复端点 GET /artifacts 的清单
// 条目（§3.8 响应形状）；speech/video 无产物时为 null。v1.2 起 images 字段退
// 役——图片历史改走 GET /images/jobs（§3.4.3）。
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
	Speech *artifactsSpeechEntry `json:"speech"`
	Video  *artifactsVideoEntry  `json:"video"`
}

// ArtifactStore 是产物暂存后端（导出接口 + 未导出方法：只允许本包提供实现，
// 生产装配 PostgreSQL，测试注入文件/内存实现）。nil 实现 = 暂存面未接线，
// 产物端点显式降级 503（设计 §1.1 功能不设开关）。
type ArtifactStore interface {
	available() bool
	// saveImageReference 落一张参考图（kind='image_reference'，孤儿态
	// job_id=''；任务创建时事务内绑定 job_id）。
	saveImageReference(viewerID string, image decodedImage) (string, error)
	// saveImageResult 落一张生成结果图（kind='image_result'，job_id=任务 id；
	// 保留期 7d 由 jobs 清理任务按任务到期删字节）。
	saveImageResult(viewerID, jobID string, image decodedImage) (string, error)
	replaceSpeech(viewerID string, data []byte, ext string) (string, error)
	replaceVideoTask(viewerID string, ref videoTaskRef) error
	list(viewerID string) (*artifactsResponse, error)
	openArtifact(viewerID, id string) (*artifactContent, error)
	cleanupExpired()
}

// playgroundArtifactsTable 是业务库暂存表（DDL 见 maintenance
// pg_schema_business_tables.go）；24h 清理按 created_at 索引全表删除。
const playgroundArtifactsTable = "juhe_business.playground_artifacts"

// pgArtifactStore 是 PostgreSQL 暂存后端。mu 串行化同进程的覆盖替换与懒清理；
// 跨节点由数据库行隔离，读路径无锁（展示缓存允许读到半新状态）。
type pgArtifactStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewPGArtifactStore 构造生产暂存后端；db 为 nil 时 available() = false。
func NewPGArtifactStore(db *sql.DB) ArtifactStore {
	return &pgArtifactStore{db: db}
}

func (s *pgArtifactStore) available() bool { return s != nil && s.db != nil }

// validateArtifactViewer 复核登录态身份（身份来自 RequireSession 后的
// ScopeResolver，此处是跨用户隔离的防线复核）。
func validateArtifactViewer(viewerID string) error {
	if !sanitizeArtifactID(viewerID) {
		return errArtifactViewerInvalid
	}
	return nil
}

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

// newArtifactID 生成产物 id：{unixNano}-{8 字节随机 hex}。
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

// saveImageReference 落一张参考图（§3.4.1：kind='image_reference'，孤儿态
// job_id=”；绑定在任务创建事务内完成）。单行 INSERT，无覆盖语义（每张独立
// id，参考图可复用跨任务）。
func (s *pgArtifactStore) saveImageReference(viewerID string, image decodedImage) (string, error) {
	return s.saveImageArtifact(viewerID, "", "image_reference", image)
}

// saveImageResult 落一张生成结果图（§3.8：kind='image_result'，
// job_id=任务 id；字节保留 7 天按任务，删除由 jobs 清理任务执行）。
func (s *pgArtifactStore) saveImageResult(viewerID, jobID string, image decodedImage) (string, error) {
	return s.saveImageArtifact(viewerID, jobID, "image_result", image)
}

// saveImageArtifact 是两类图像产物（参考图/结果图）的共用写入路径。
func (s *pgArtifactStore) saveImageArtifact(viewerID, jobID, kind string, image decodedImage) (string, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return "", err
	}
	if !s.available() {
		return "", errArtifactStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, idErr := newArtifactID()
	if idErr != nil {
		return "", idErr
	}
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,job_id,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		viewerID, id, kind, image.MIME, image.Data, jobID, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	return id, nil
}

// replaceSpeech 同 kind 覆盖写入语音产物：事务内删旧音频、写新条（ext 来自
// 请求 format 词表，缺省 mp3），返回产物 id。
func (s *pgArtifactStore) replaceSpeech(viewerID string, data []byte, ext string) (string, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return "", err
	}
	if !s.available() {
		return "", errArtifactStoreUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND kind='speech'`, viewerID); err != nil {
		return "", err
	}
	id, idErr := newArtifactID()
	if idErr != nil {
		return "", idErr
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,created_at) VALUES ($1,$2,'speech',$3,$4,$5)`,
		viewerID, id, artifactMIMEByExtension(ext), data, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// replaceVideoTask 同 kind 覆盖写入视频任务引用（单条 JSON，产物本体不落盘）。
func (s *pgArtifactStore) replaceVideoTask(viewerID string, ref videoTaskRef) error {
	if err := validateArtifactViewer(viewerID); err != nil {
		return err
	}
	if !s.available() {
		return errArtifactStoreUnavailable
	}
	payload, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND kind='video_task'`, viewerID); err != nil {
		return err
	}
	id, idErr := newArtifactID()
	if idErr != nil {
		return idErr
	}
	if _, err := tx.ExecContext(context.Background(),
		`INSERT INTO `+playgroundArtifactsTable+` (system_account_id,id,kind,mime_type,data,created_at) VALUES ($1,$2,'video_task','application/json',$3,$4)`,
		viewerID, id, payload, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// list 读取当前用户暂存清单（§3.8 恢复端点形状，v1.2 起无 images 字段）：
// speech 取最新一条音频；video 取最新任务引用（JSON 解析失败视为无引用——展
// 示缓存不因脏行失败）。无产物返回 null 组。
func (s *pgArtifactStore) list(viewerID string) (*artifactsResponse, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return nil, err
	}
	if !s.available() {
		return nil, errArtifactStoreUnavailable
	}
	out := &artifactsResponse{}
	cutoff := time.Now().Add(-artifactsRetention).UnixMilli()
	var speechID, speechMIME string
	var speechCreatedAt int64
	speechErr := s.db.QueryRowContext(context.Background(),
		`SELECT id,mime_type,created_at FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND kind='speech' AND created_at >= $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
		viewerID, cutoff).Scan(&speechID, &speechMIME, &speechCreatedAt)
	if speechErr == nil {
		out.Speech = &artifactsSpeechEntry{
			ID: speechID, URL: artifactContentURL(speechID), MimeType: speechMIME, CreatedAt: speechCreatedAt / 1000,
		}
	} else if !errors.Is(speechErr, sql.ErrNoRows) {
		return nil, speechErr
	}
	var videoPayload []byte
	videoErr := s.db.QueryRowContext(context.Background(),
		`SELECT data FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND kind='video_task' AND created_at >= $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
		viewerID, cutoff).Scan(&videoPayload)
	if videoErr == nil {
		var ref videoTaskRef
		if json.Unmarshal(videoPayload, &ref) == nil {
			out.Video = &artifactsVideoEntry{
				JobID: ref.JobID, AccountID: ref.AccountID, ModelID: ref.ModelID, Status: ref.Status, CreatedAt: ref.CreatedAt,
			}
		}
	} else if !errors.Is(videoErr, sql.ErrNoRows) {
		return nil, videoErr
	}
	return out, nil
}

// artifactContent 是 content 端点的读取产物（文件名供 ServeContent 兜底嗅探、
// MIME 按扩展名词表预置、ModTime 供条件请求）。
type artifactContent struct {
	Name    string
	MIME    string
	ModTime time.Time
	Data    []byte
}

// openArtifact 按 id 定位本用户媒体产物（kind=image_result/image_reference/
// speech；视频任务引用一律 errArtifactNotFound——视频内容走 §3.5 jobId 代理，
// 不经此端点。v1.0 退役的 kind='image' 行不再开放，存量由 jobs 清理任务删除）。
func (s *pgArtifactStore) openArtifact(viewerID, id string) (*artifactContent, error) {
	if err := validateArtifactViewer(viewerID); err != nil {
		return nil, err
	}
	if !s.available() {
		return nil, errArtifactStoreUnavailable
	}
	var mimeType string
	var data []byte
	var createdAt int64
	err := s.db.QueryRowContext(context.Background(),
		`SELECT mime_type,data,created_at FROM `+playgroundArtifactsTable+` WHERE system_account_id=$1 AND id=$2 AND kind IN ('image_result','image_reference','speech')`,
		viewerID, id).Scan(&mimeType, &data, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(data) > artifactContentMaxBytes {
		return nil, errArtifactNotFound
	}
	return &artifactContent{
		Name:    id + "." + mimeExtension(mimeType),
		MIME:    mimeType,
		ModTime: time.UnixMilli(createdAt),
		Data:    data,
	}, nil
}

// cleanupExpired 是 24h 懒清理（§3.8 v1.2 口径）：只删 speech/video_task 与
// 孤儿参考图（kind='image_reference' AND job_id=”）三类过期行；绑定任务的
// image_result/image_reference 由 jobs 项目定时任务按 7 天保留期删字节并置
// images_expired（§3.8），此处不动。挂产物读写点（POST references、
// GET artifacts、GET images/jobs、GET content）同步执行；清理失败静默吞掉
// （缓存 GC 失败不得阻断主生成路径，下一读写点自然重试）。
func (s *pgArtifactStore) cleanupExpired() {
	if !s.available() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-artifactsRetention).UnixMilli()
	_, _ = s.db.ExecContext(context.Background(),
		`DELETE FROM `+playgroundArtifactsTable+` WHERE created_at < $1
		 AND (kind IN ('speech','video_task') OR (kind='image_reference' AND job_id=''))`, cutoff)
}

// artifactsReady 报告产物暂存后端是否已接线（nil 接口 = 未接线，端点显式
// 降级 503，功能不设开关）。
func (rt *playgroundRoutes) artifactsReady() bool {
	return rt.artifacts != nil && rt.artifacts.available()
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
	if !rt.artifactsReady() {
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
	if !rt.artifactsReady() {
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

// artifactsUnavailable 是暂存面未接线（后端为 nil）的显式降级。
func artifactsUnavailable() *HTTPError {
	return &HTTPError{Status: http.StatusServiceUnavailable, Code: codeEndpointDegraded, Message: "产物暂存暂不可用，请稍后重试"}
}
