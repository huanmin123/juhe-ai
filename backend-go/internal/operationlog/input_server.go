package operationlog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const InputPath = "/__aiinternal__/v1/operation-logs"
const ListPath = "/__aiinternal__/v1/operation-logs/list"
const DetailPath = "/__aiinternal__/v1/operation-logs/detail/"
const HealthPath = "/__aiinternal__/v1/operation-logs/health"
const SignatureHeader = "X-Juhe-AI-Signature"
const TimestampHeader = "X-Juhe-AI-Timestamp"
const NonceHeader = "X-Juhe-AI-Nonce"
const signatureDomain = "juhe-ai/operation-log-input/v1"

type envelope struct {
	SchemaVersion int   `json:"schemaVersion"`
	OperationLog  Input `json:"operationLog"`
}
type listRequest struct {
	Options ListOptions `json:"options"`
}
type detailRequest struct {
	ViewerID string `json:"viewerId,omitempty"`
}

func SignInput(secret, timestamp, nonce string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write([]byte(signatureDomain))
	_, _ = m.Write([]byte{'\n'})
	_, _ = m.Write([]byte(timestamp))
	_, _ = m.Write([]byte{'\n'})
	_, _ = m.Write([]byte(nonce))
	_, _ = m.Write([]byte{'\n'})
	_, _ = m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}
func validSignature(secret, timestamp, nonce string, body []byte, supplied string) bool {
	if !strings.HasPrefix(supplied, "v1=") {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(supplied, "v1="))
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(SignInput(secret, timestamp, nonce, body), "v1="))
	return err == nil && hmac.Equal(expected, actual)
}

func RunInputServer(ctx context.Context, store Store, cfg Config, inputCfg InputServerConfig, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	lease, ok, err := store.AcquireOwnerLease(ctx, cfg.InstanceID, cfg.OwnerLease)
	if err != nil {
		return fmt.Errorf("F4 acquire owner lease: %w", err)
	}
	if !ok {
		return fmt.Errorf("F4 operation log owner lease held by another sidecar")
	}
	defer store.ReleaseOwnerLease(context.Background(), lease)
	listener, err := net.Listen("tcp", inputCfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen F4 operation log: %w", err)
	}
	defer listener.Close()
	healthy := &atomic.Bool{}
	healthy.Store(true)
	h := &handler{store: store, lease: lease, cfg: inputCfg, logger: logger, healthy: healthy}
	server := &http.Server{Handler: h, ReadHeaderTimeout: inputCfg.RequestTimeout, ReadTimeout: inputCfg.RequestTimeout, WriteTimeout: inputCfg.RequestTimeout, IdleTimeout: 30 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	retention := time.NewTicker(cfg.RetentionInterval)
	defer retention.Stop()
	renew := time.NewTicker(cfg.OwnerLease / 3)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(shutdownCtx)
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-renew.C:
			ok, err := store.RenewOwnerLease(ctx, lease, cfg.OwnerLease)
			if err != nil {
				return err
			}
			if !ok {
				return ErrOwnerLeaseLost
			}
		case <-retention.C:
			days, err := store.RetentionDays(ctx, cfg.RetentionDays)
			if err != nil {
				logger.Error("F4 operation log retention setting unavailable; skip pass", "error", err)
				continue
			}
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			deleted, err := store.CleanupRetention(ctx, lease, cutoff, cfg.RetentionBatchSize)
			if err != nil {
				logger.Error("F4 operation log retention failed", "error", err, "mode", cfg.Mode, "cutoff", cutoff.Format(time.RFC3339Nano))
			} else {
				logger.Info("F4 operation log retention complete", "mode", cfg.Mode, "retentionDays", days, "cutoff", cutoff.Format(time.RFC3339Nano), "deleted", deleted)
			}
		}
	}
}

type handler struct {
	store   Store
	lease   OwnerLease
	cfg     InputServerConfig
	logger  *slog.Logger
	healthy *atomic.Bool
	replays replayCache
}

type replayCache struct {
	mu     sync.Mutex
	nonces map[string]time.Time
}

func (c *replayCache) accept(nonce string, now time.Time, window time.Duration) bool {
	if nonce == "" || len(nonce) > 128 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nonces == nil {
		c.nonces = map[string]time.Time{}
	}
	for value, expiry := range c.nonces {
		if !expiry.After(now) {
			delete(c.nonces, value)
		}
	}
	if _, exists := c.nonces[nonce]; exists {
		return false
	}
	c.nonces[nonce] = now.Add(window)
	return true
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loopback(r.RemoteAddr) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.URL.Path == HealthPath && r.Method == http.MethodGet {
		if h.healthy.Load() {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if r.ContentLength < 0 || r.ContentLength > h.cfg.MaxBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	timestamp := r.Header.Get(TimestampHeader)
	nonce := r.Header.Get(NonceHeader)
	parsedTimestamp, timestampErr := time.Parse(time.RFC3339Nano, timestamp)
	replayWindow := h.cfg.ReplayWindow
	if replayWindow <= 0 {
		replayWindow = 5 * time.Minute
	}
	if timestampErr != nil || time.Since(parsedTimestamp).Abs() > replayWindow {
		if timestampErr == nil {
			timestampErr = errors.New("request timestamp outside replay window")
		}
		h.logRejectedInput("timestamp_invalid_or_expired", body, timestampErr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !validSignature(h.cfg.SharedSecret, timestamp, nonce, body, r.Header.Get(SignatureHeader)) {
		h.logRejectedInput("signature_invalid", body, errors.New("signature mismatch"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !h.replays.accept(nonce, time.Now().UTC(), replayWindow) {
		h.logRejectedInput("nonce_replayed", body, errors.New("nonce is missing, malformed, or already used"))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == InputPath:
		h.write(w, body)
	case r.URL.Path == ListPath:
		h.list(w, body)
	case strings.HasPrefix(r.URL.Path, DetailPath):
		h.detail(w, body, strings.TrimPrefix(r.URL.Path, DetailPath))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
func (h *handler) write(w http.ResponseWriter, body []byte) {
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil || e.SchemaVersion != 1 {
		h.logRejectedInput("json_invalid", body, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.RequestTimeout)
	defer cancel()
	_, err := h.store.Persist(ctx, h.lease, e.OperationLog)
	if err != nil {
		h.logger.Error("F4 operation log single record rejected", "error", err, "operationLogID", e.OperationLog.ID)
		if errors.Is(err, ErrOwnerLeaseLost) {
			h.healthy.Store(false)
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) logRejectedInput(reason string, body []byte, err error) {
	var e envelope
	_ = json.Unmarshal(body, &e)
	fields := []any{"reason", reason, "operationLogID", e.OperationLog.ID}
	if err != nil {
		fields = append(fields, "error", err)
	}
	h.logger.Warn("F4 operation log request rejected", fields...)
}
func (h *handler) list(w http.ResponseWriter, body []byte) {
	var request listRequest
	if json.Unmarshal(body, &request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.RequestTimeout)
	defer cancel()
	result, err := h.store.List(ctx, request.Options)
	if err != nil {
		h.logger.Error("F4 operation log list failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
func (h *handler) detail(w http.ResponseWriter, body []byte, id string) {
	var request detailRequest
	if json.Unmarshal(body, &request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.RequestTimeout)
	defer cancel()
	result, found, err := h.store.Detail(ctx, id, request.ViewerID)
	if err != nil {
		h.logger.Error("F4 operation log detail failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, result)
}
func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func loopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
