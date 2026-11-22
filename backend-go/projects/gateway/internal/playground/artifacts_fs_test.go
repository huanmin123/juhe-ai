package playground

// 文件系统暂存后端（仅测试用）：保留 2026-11-09 之前的生产实现作为单元测试
// 替身，让 handler 行为断言（覆盖替换/24h 懒清理/归属）不依赖真实 PG。
// 生产装配固定为 NewPGArtifactStore(composed.db)。

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type fsArtifactStore struct {
	root string
	mu   sync.Mutex
}

func newFSArtifactStore(root string) ArtifactStore {
	return &fsArtifactStore{root: strings.TrimSpace(root)}
}

func (s *fsArtifactStore) available() bool { return s.root != "" }

func (s *fsArtifactStore) userDir(viewerID string) (string, error) {
	if !sanitizeArtifactID(viewerID) {
		return "", errArtifactViewerInvalid
	}
	return filepath.Join(s.root, viewerID), nil
}

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

// saveImageReference / saveImageResult 是图像产物的文件替身写入（§3.4 v1.2：
// 每张独立 id，无覆盖语义；job_id 绑定是 PG 列语义，文件替身不承载——孤儿清
// 理的 kind 区分由 PG 测试覆盖）。
func (s *fsArtifactStore) saveImageReference(viewerID string, image decodedImage) (string, error) {
	return s.saveImageFile(viewerID, image)
}

func (s *fsArtifactStore) saveImageResult(viewerID string, _ string, image decodedImage) (string, error) {
	return s.saveImageFile(viewerID, image)
}

func (s *fsArtifactStore) saveImageFile(viewerID string, image decodedImage) (string, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, artifactsDirPerm); err != nil {
		return "", err
	}
	id, idErr := newArtifactID()
	if idErr != nil {
		return "", idErr
	}
	name := id + "." + mimeExtension(image.MIME)
	if err := os.WriteFile(filepath.Join(dir, name), image.Data, artifactsFilePerm); err != nil {
		return "", err
	}
	return id, nil
}

func (s *fsArtifactStore) replaceSpeech(viewerID string, data []byte, ext string) (string, error) {
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

func (s *fsArtifactStore) replaceVideoTask(viewerID string, ref videoTaskRef) error {
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

func (s *fsArtifactStore) list(viewerID string) (*artifactsResponse, error) {
	dir, err := s.userDir(viewerID)
	if err != nil {
		return nil, err
	}
	out := &artifactsResponse{}
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
		// v1.2 起 GET /artifacts 无 images 字段（图片历史走 GET /images/jobs，
		// §3.8）；图像文件只服务 content 取流（image_result/image_reference）。
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
	if speech != nil {
		out.Speech = &artifactsSpeechEntry{
			ID: speech.id, URL: artifactContentURL(speech.id),
			MimeType: artifactAudioMIME[speech.ext], CreatedAt: speech.scope.Unix(),
		}
	}
	if video != nil {
		ref, refErr := readVideoTaskRefFile(dir, video.name)
		if refErr == nil {
			out.Video = &artifactsVideoEntry{
				JobID: ref.JobID, APIKeyID: ref.APIKeyID, ModelID: ref.ModelID,
				Status: ref.Status, CreatedAt: ref.CreatedAt,
			}
		}
	}
	return out, nil
}

func readVideoTaskRefFile(dir, name string) (videoTaskRef, error) {
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

func (s *fsArtifactStore) openArtifact(viewerID, id string) (*artifactContent, error) {
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

// cleanupExpired 是文件替身的 24h 懒清理近似：删过期 audio/json + 过期图像
// 文件。PG 侧图像文件按 kind 区分孤儿参考图（job_id=”）与绑定产物（jobs 清
// 理任务职责），文件替身无该列，统一按孤儿口径近似（绑定产物语义由 PG 测试
// 覆盖，见 imagejob_store_pg_test.go）。
func (s *fsArtifactStore) cleanupExpired() {
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
