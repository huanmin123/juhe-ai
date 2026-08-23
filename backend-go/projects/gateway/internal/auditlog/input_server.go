package auditlog

import (
	"bytes"
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
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AuditInputPath                        = "/__aiinternal__/v1/audit-captures"
	AuditInputHealthPath                  = "/__aiinternal__/health"
	AuditInputSignatureHeader             = "X-Juhe-AI-Signature"
	AuditInputTimestampHeader             = "X-Juhe-AI-Timestamp"
	AuditInputNonceHeader                 = "X-Juhe-AI-Nonce"
	auditInputSignatureDomain             = "juhe-ai/audit-log-input/v1"
	defaultInputMaxBytes            int64 = 4 << 20
	defaultInputTimeout                   = 5 * time.Second
	defaultInputReplayWindow              = 5 * time.Minute
	defaultInputReplayCacheCapacity       = 4096
	minimumProductionInputSecretLen       = 32
)

// InputServerConfig is intentionally separate from the persistence config.
// A foundation-only Store can still be constructed for migrations and smoke
// tests, while a running F3 owner must explicitly opt into a loopback input.
type InputServerConfig struct {
	ListenAddress       string
	SharedSecret        string
	MaxBytes            int64
	RequestTimeout      time.Duration
	ReplayWindow        time.Duration
	ReplayCacheCapacity int
}

func LoadInputServerConfig(getenv func(string) string) (InputServerConfig, error) {
	cfg := InputServerConfig{
		ListenAddress:       strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS")),
		SharedSecret:        strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INPUT_SECRET")),
		MaxBytes:            defaultInputMaxBytes,
		RequestTimeout:      defaultInputTimeout,
		ReplayWindow:        defaultInputReplayWindow,
		ReplayCacheCapacity: defaultInputReplayCacheCapacity,
	}
	if cfg.ListenAddress == "" {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS 是 F3 sidecar 的必填配置")
	}
	if err := validateLoopbackAddress(cfg.ListenAddress); err != nil {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS 必须是 loopback IP:port: %w", err)
	}
	if cfg.SharedSecret == "" {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_SECRET 是 F3 loopback HMAC 的必填配置")
	}
	if strings.EqualFold(strings.TrimSpace(getenv("NODE_ENV")), "production") && len(cfg.SharedSecret) < minimumProductionInputSecretLen {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_SECRET 在 production 环境必须至少 %d 位", minimumProductionInputSecretLen)
	}
	if raw := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INPUT_MAX_BYTES")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1024 || parsed > defaultInputMaxBytes {
			return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_MAX_BYTES 必须在 1024..%d", defaultInputMaxBytes)
		}
		cfg.MaxBytes = parsed
	}
	if raw := strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > 30*time.Second {
			return InputServerConfig{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT 必须为不超过 30s 的正 duration")
		}
		cfg.RequestTimeout = parsed
	}
	return cfg, nil
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("地址必须为 IP:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("端口必须在 1..65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q 不是 loopback IP", host)
	}
	return nil
}

type auditInputEnvelope struct {
	SchemaVersion int           `json:"schemaVersion"`
	AuditLog      AuditLogInput `json:"auditLog"`
}

// RunInputServer binds only a loopback address, keeps an owner lease alive,
// and performs the audit write synchronously in the request goroutine. It is
// an RPC handoff, not a queue: a 204 means the F3 store transaction finished.
func RunInputServer(ctx context.Context, store Store, cfg Config, inputCfg InputServerConfig, logger *slog.Logger) error {
	if err := cfg.validateRetentionPolicy(); err != nil {
		return fmt.Errorf("F3 audit retention 配置无效: %w", err)
	}
	if replayWindow := effectiveAuditInputReplayWindow(inputCfg); replayWindow >= time.Duration(cfg.ProblemRetentionDays)*24*time.Hour {
		return fmt.Errorf("F3 audit input replay window 必须小于 problem retention")
	}
	logger = loggerOrDefault(logger)
	lease, acquired, err := store.AcquireOwnerLease(ctx, cfg.InstanceID, cfg.OwnerLease)
	if err != nil {
		return fmt.Errorf("获取 F3 audit owner lease 失败: %w", err)
	}
	if !acquired {
		return fmt.Errorf("F3 audit owner lease 已被其他实例持有")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := store.ReleaseOwnerLease(releaseCtx, lease); releaseErr != nil {
			logger.Error("释放 F3 audit owner lease 失败", "error", releaseErr)
		}
	}()

	listener, err := net.Listen("tcp", inputCfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("监听 F3 loopback input 失败: %w", err)
	}
	defer listener.Close()
	healthy := &atomic.Bool{}
	healthy.Store(true)
	componentFatal := make(chan error, 1)
	handler := &auditInputHandler{
		store:          store,
		lease:          lease,
		cfg:            inputCfg,
		logger:         logger,
		healthy:        healthy,
		componentFatal: componentFatal,
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: inputCfg.RequestTimeout,
		ReadTimeout:       inputCfg.RequestTimeout,
		WriteTimeout:      inputCfg.RequestTimeout,
		IdleTimeout:       30 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				healthy.Store(false)
				serveResult <- fmt.Errorf("F3 loopback input server goroutine panic: %v\n%s", recovered, debug.Stack())
			}
		}()
		serveResult <- server.Serve(listener)
	}()
	maintenanceCtx, stopMaintenance := context.WithCancel(ctx)
	maintenanceFatal := make(chan error, 1)
	maintenanceDone := runRetentionMaintenance(maintenanceCtx, store, lease, cfg, logger, healthy, maintenanceFatal)
	defer func() {
		stopMaintenance()
		<-maintenanceDone
	}()

	interval := cfg.OwnerLease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return shutdownInputServer(server, logger)
		case err := <-serveResult:
			healthy.Store(false)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("F3 loopback input server 异常退出: %w", err)
		case err := <-componentFatal:
			healthy.Store(false)
			_ = shutdownInputServer(server, logger)
			return fmt.Errorf("F3 audit input 组件异常: %w", err)
		case err := <-maintenanceFatal:
			healthy.Store(false)
			_ = shutdownInputServer(server, logger)
			return fmt.Errorf("F3 audit retention 组件异常: %w", err)
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, minDuration(5*time.Second, interval))
			renewed, renewErr := store.RenewOwnerLease(renewCtx, lease, cfg.OwnerLease)
			cancel()
			if renewErr != nil {
				healthy.Store(false)
				_ = shutdownInputServer(server, logger)
				return fmt.Errorf("续租 F3 audit owner lease 失败: %w", renewErr)
			}
			if !renewed {
				healthy.Store(false)
				_ = shutdownInputServer(server, logger)
				return ErrOwnerLeaseLost
			}
		}
	}
}

// runRetentionMaintenance keeps one bounded maintenance pass per configured
// interval. Retention also removes completed hot-search buckets, so both
// cleanup surfaces share the same owner fence and do not need a second task.
func runRetentionMaintenance(ctx context.Context, store Store, lease OwnerLease, cfg Config, logger *slog.Logger, healthy *atomic.Bool, fatal chan<- error) <-chan struct{} {
	logger = loggerOrDefault(logger)
	done := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("F3 audit retention maintenance goroutine panic: %v\n%s", recovered, debug.Stack())
				failInputComponent(healthy, ctx, fatal, err)
			}
			close(done)
		}()
		ticker := time.NewTicker(cfg.RetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := store.CleanupRetention(ctx, lease, cfg.RetentionConfigAt(time.Now()))
				if err == nil {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, ErrOwnerLeaseLost) {
					failInputComponent(healthy, ctx, fatal, err)
					return
				}
				logger.Error("F3 audit retention maintenance failed", "error", err)
				failInputComponent(healthy, ctx, fatal, fmt.Errorf("F3 audit retention maintenance failed: %w", err))
				return
			}
		}
	}()
	return done
}

func shutdownInputServer(server *http.Server, logger *slog.Logger) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("关闭 F3 loopback input server 失败", "error", err)
		return err
	}
	return nil
}

func minDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}

type auditInputHandler struct {
	store          Store
	lease          OwnerLease
	cfg            InputServerConfig
	logger         *slog.Logger
	healthy        *atomic.Bool
	componentFatal chan<- error
	replays        replayCache
}

type replayCache struct {
	mu     sync.Mutex
	nonces map[string]time.Time
}

func effectiveAuditInputReplayWindow(cfg InputServerConfig) time.Duration {
	if cfg.ReplayWindow > 0 {
		return cfg.ReplayWindow
	}
	return defaultInputReplayWindow
}

func effectiveAuditInputReplayCacheCapacity(cfg InputServerConfig) int {
	if cfg.ReplayCacheCapacity > 0 {
		return cfg.ReplayCacheCapacity
	}
	return defaultInputReplayCacheCapacity
}

func (c *replayCache) accept(nonce string, now time.Time, window time.Duration, capacity int) bool {
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
	if _, exists := c.nonces[nonce]; exists || len(c.nonces) >= capacity {
		return false
	}
	c.nonces[nonce] = now.Add(window)
	return true
}

func reportInputComponentFatal(ctx context.Context, fatal chan<- error, err error) {
	if fatal == nil {
		return
	}
	select {
	case fatal <- err:
	case <-ctx.Done():
	default:
	}
}

func (h *auditInputHandler) failComponent(err error) {
	failInputComponent(h.healthy, context.Background(), h.componentFatal, err)
}

func failInputComponent(healthy *atomic.Bool, ctx context.Context, fatal chan<- error, err error) {
	if healthy != nil {
		healthy.Store(false)
	}
	reportInputComponentFatal(ctx, fatal, err)
}

func (h *auditInputHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			loggerOrDefault(h.logger).Error("F3 audit input request panic", "error", fmt.Errorf("%v", recovered), "stack", string(debug.Stack()))
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}()
	if request.URL.Path == AuditInputHealthPath && request.Method == http.MethodGet {
		if !isLoopbackRemote(request.RemoteAddr) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		if h.healthy != nil && !h.healthy.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.URL.Path != AuditInputPath || request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if !isLoopbackRemote(request.RemoteAddr) {
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writer.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if request.ContentLength < 0 || request.ContentLength > h.cfg.MaxBytes {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, h.cfg.MaxBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	timestamp := request.Header.Get(AuditInputTimestampHeader)
	nonce := request.Header.Get(AuditInputNonceHeader)
	parsedTimestamp, timestampErr := time.Parse(time.RFC3339Nano, timestamp)
	replayWindow := effectiveAuditInputReplayWindow(h.cfg)
	if timestampErr != nil || time.Since(parsedTimestamp).Abs() > replayWindow {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !validAuditInputSignature(h.cfg.SharedSecret, timestamp, nonce, body, request.Header.Get(AuditInputSignatureHeader)) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !h.replays.accept(nonce, time.Now().UTC(), replayWindow, effectiveAuditInputReplayCacheCapacity(h.cfg)) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	var envelope auditInputEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.SchemaVersion != 1 {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	normalized, err := normalizeAuditInput(envelope.AuditLog)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	// Node deliberately makes a non-blocking one-shot RPC and may abandon the
	// response before F3 commits. Persistence remains bounded by the F3 server
	// deadline, not by the peer connection's cancellation.
	writeCtx, cancel := context.WithTimeout(context.Background(), h.cfg.RequestTimeout)
	defer cancel()
	result, err := h.store.Persist(writeCtx, h.lease, normalized)
	if err != nil {
		if errors.Is(err, ErrOwnerLeaseLost) {
			h.failComponent(fmt.Errorf("F3 audit input owner lease 丢失: %w", err))
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		loggerOrDefault(h.logger).Error("F3 audit input 持久化失败", "error", err, "traceID", envelope.AuditLog.TraceID, "auditLogID", envelope.AuditLog.ID)
		if errors.Is(err, context.DeadlineExceeded) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	logger := loggerOrDefault(h.logger)
	if !result.Ignored {
		if _, appendErr := h.store.AppendHotSearch(writeCtx, h.lease, []AuditLogInput{normalized}); appendErr != nil {
			// The audit row is already durable. The mirror can be rebuilt from the
			// canonical store, so this request-level failure must not take the input
			// listener down or turn one bad hot-search write into an audit outage.
			logger.Error("F3 audit hot-search append failed", "error", appendErr, "traceID", normalized.TraceID, "auditLogID", normalized.ID)
		}
	}
	logger.Debug("F3 audit input 已持久化", "auditLogID", envelope.AuditLog.ID, "ignored", result.Ignored)
	writer.WriteHeader(http.StatusNoContent)
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

func isLoopbackRemote(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func SignAuditInput(secret, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(auditInputSignatureDomain))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(nonce))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func validAuditInputSignature(secret, timestamp, nonce string, body []byte, supplied string) bool {
	if !strings.HasPrefix(supplied, "v1=") {
		return false
	}
	expected := hmac.New(sha256.New, []byte(secret))
	_, _ = expected.Write([]byte(auditInputSignatureDomain))
	_, _ = expected.Write([]byte{'\n'})
	_, _ = expected.Write([]byte(timestamp))
	_, _ = expected.Write([]byte{'\n'})
	_, _ = expected.Write([]byte(nonce))
	_, _ = expected.Write([]byte{'\n'})
	_, _ = expected.Write(body)
	actual, err := hex.DecodeString(strings.TrimPrefix(supplied, "v1="))
	return err == nil && hmac.Equal(expected.Sum(nil), actual)
}
