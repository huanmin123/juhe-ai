package internalapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// account-health-check-dispatch 接管，逐语义对齐 Node
// modules/internal-api/account-health-check-dispatch.service.ts 与
// account-health-jobs-dispatch-boundary.ts + account-health-jobs-input.protocol.ts：
//   - Go 只发布不可变请求事实（signed request file）；Go jobs 是唯一 J1 任务
//     拥有者，此路径从不调用 worker/Gateway/Redis Stream/HTTP bridge；
//   - outcome: queued | rejected(dispatch_rejected | input_unavailable)；
//   - 账户不在 J1 冻结范围（无 J1 input epoch）不算失败：静默跳过并结算
//     source fence = unknown。

const (
	healthInputSignatureAlgorithm = "hmac-sha256-v1"
	healthInputSignatureKeyID     = "runtime-v1"
	healthRequestFileSuffix       = ".account-health-request.json"
	// HealthCheckProbeRequestIDPrefix 对齐 Node `j1-${randomUUID()}`。
	HealthCheckProbeRequestIDPrefix = "j1-"
)

// HealthCheckAccountRef 是 boundary 返回的账户事实。
type HealthCheckAccountRef struct {
	ID               string
	ConfigRevision   int64
	DispatchRevision int64
}

// HealthCheckRevisions 是 J1 input epoch 事实。
type HealthCheckRevisions struct {
	ConfigRevision   int64
	DispatchRevision int64
}

// HealthCheckSourceFence 对齐 CodexSourceProbeFence 窄投影。
type HealthCheckSourceFence struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
	RuntimeKey       string
	ProbeGeneration  int64
	ConfigRevision   int64
}

// HealthCheckKeyModelFence 对齐 KeyModelFenceReference 窄投影。
type HealthCheckKeyModelFence struct {
	CapabilityHash   string
	KeyFingerprint   string
	DispatchRevision int64
	OwnerID          string
}

// HealthCheckBoundary 是账户事实读取 port（gateway 组合根注入实现）。
type HealthCheckBoundary interface {
	// CurrentProbeInput 返回账户与其 J1 input/revisions；ok=false 表示账户
	// 不在 J1 冻结范围或 input epoch 缺失。
	CurrentProbeInput(ctx context.Context, accountID string) (account HealthCheckAccountRef, inputVersion int64, revisions HealthCheckRevisions, ok bool, err error)
}

// SourceFenceSettler 结算 source fence；state 取 "unknown"。
type SourceFenceSettler func(ctx context.Context, fence HealthCheckSourceFence, state string) error

// HealthCheckDispatchOptions 组合根配置。
type HealthCheckDispatchOptions struct {
	// InputRoot 与 SigningKey 对齐 runtimeConfig.accountHealthJobs.inputDirectory/inputSigningKey。
	InputRoot         string
	SigningKey        string
	ProbeDeadlineMS   int64
	NowMS             func() int64
	Boundary          HealthCheckBoundary
	SettleSourceFence SourceFenceSettler
}

// HealthCheckDispatchOutcome 与 Node 完全一致。
type HealthCheckDispatchOutcome struct {
	Outcome      string `json:"outcome"`      // queued | rejected
	DecisionCode string `json:"decisionCode"` // queued | dispatch_rejected | input_unavailable
	TargetRole   string `json:"targetRole"`   // go-jobs
	RequestID    string `json:"requestId,omitempty"`
}

func rejectedDispatchOutcome(decisionCode string) HealthCheckDispatchOutcome {
	return HealthCheckDispatchOutcome{Outcome: "rejected", DecisionCode: decisionCode, TargetRole: "go-jobs"}
}

// DispatchAccountHealthCheckWithOutcome 对齐 dispatchAccountHealthCheckWithOutcome。
// 与 Node 不同：Node 是 fire-and-forget 异步发布（立即返回 queued），发布失败
// 只留 warn 日志；Go 形态在同一调用内同步发布并返回可观察结果，失败即
// rejected，调用方可重试。这是接管后的行为差异点（见报告）。
func DispatchAccountHealthCheckWithOutcome(ctx context.Context, accountID, reason, traceID string, sourceFence *HealthCheckSourceFence, keyModelFence *HealthCheckKeyModelFence, options HealthCheckDispatchOptions) (HealthCheckDispatchOutcome, error) {
	normalizedID := strings.TrimSpace(accountID)
	if normalizedID == "" {
		return rejectedDispatchOutcome("dispatch_rejected"), nil
	}
	root := strings.TrimSpace(options.InputRoot)
	signingKey := strings.TrimSpace(options.SigningKey)
	if root == "" || signingKey == "" {
		return rejectedDispatchOutcome("input_unavailable"), nil
	}
	if options.Boundary == nil || options.NowMS == nil {
		return HealthCheckDispatchOutcome{}, errors.New("账户健康检查派发依赖未初始化")
	}
	requestID := HealthCheckProbeRequestIDPrefix + newUUID()
	account, inputVersion, revisions, ok, err := options.Boundary.CurrentProbeInput(ctx, normalizedID)
	if err != nil {
		return HealthCheckDispatchOutcome{}, err
	}
	if !ok || inputVersion < 1 || revisions.ConfigRevision != account.ConfigRevision {
		// 冻结 J1 范围之外的账户：跳过请求发布，仍结算 source fence。
		if sourceFence != nil && options.SettleSourceFence != nil {
			if settleErr := options.SettleSourceFence(ctx, *sourceFence, "unknown"); settleErr != nil {
				return HealthCheckDispatchOutcome{}, settleErr
			}
		}
		return HealthCheckDispatchOutcome{Outcome: "queued", DecisionCode: "queued", TargetRole: "go-jobs", RequestID: requestID}, nil
	}
	deadline := options.NowMS() + options.ProbeDeadlineMS
	payload, err := buildProbeRequestPayload(probeRequestSource{
		requestID:        requestID,
		reason:           strings.TrimSpace(reason),
		accountID:        account.ID,
		configRevision:   account.ConfigRevision,
		dispatchRevision: account.DispatchRevision,
		inputVersion:     inputVersion,
		deadline:         deadline,
		nowMS:            options.NowMS(),
		sourceFence:      sourceFence,
		keyModelFence:    keyModelFence,
	})
	if err != nil {
		return HealthCheckDispatchOutcome{}, err
	}
	if _, err := publishAccountHealthJobsRequest(root, requestID, payload, signingKey); err != nil {
		return HealthCheckDispatchOutcome{}, err
	}
	return HealthCheckDispatchOutcome{Outcome: "queued", DecisionCode: "queued", TargetRole: "go-jobs", RequestID: requestID}, nil
}

// DispatchAccountHealthCheck 对齐布尔便捷包装。
func DispatchAccountHealthCheck(ctx context.Context, accountID, reason, traceID string, options HealthCheckDispatchOptions) (bool, error) {
	outcome, err := DispatchAccountHealthCheckWithOutcome(ctx, accountID, reason, traceID, nil, nil, options)
	if err != nil {
		return false, err
	}
	return outcome.Outcome != "rejected", nil
}

type probeRequestSource struct {
	requestID        string
	reason           string
	accountID        string
	configRevision   int64
	dispatchRevision int64
	inputVersion     int64
	deadline         int64
	nowMS            int64
	sourceFence      *HealthCheckSourceFence
	keyModelFence    *HealthCheckKeyModelFence
}

// buildProbeRequestPayload 对齐 Node publishAccountHealthJobsProbeRequest 的
// payload 字段与全部校验。
func buildProbeRequestPayload(source probeRequestSource) (map[string]any, error) {
	if source.accountID == "" || source.requestID == "" || source.reason == "" {
		return nil, errors.New("J1 request 缺少 accountId、requestId 或 reason")
	}
	if source.deadline <= source.nowMS {
		return nil, errors.New("J1 request deadline 必须在未来")
	}
	configRevision, err := positiveInteger(source.configRevision, "account configRevision")
	if err != nil {
		return nil, err
	}
	dispatchRevision, err := positiveInteger(source.dispatchRevision, "account dispatchRevision")
	if err != nil {
		return nil, err
	}
	inputVersion, err := positiveInteger(source.inputVersion, "J1 request inputVersion")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"request_id":        source.requestID,
		"account_id":        source.accountID,
		"reason":            source.reason,
		"input_version":     inputVersion,
		"config_revision":   configRevision,
		"dispatch_revision": dispatchRevision,
		"deadline":          time.UnixMilli(source.deadline).UTC().Format(time.RFC3339Nano),
		"mutate_account":    source.sourceFence == nil,
	}
	if source.sourceFence != nil {
		fence := source.sourceFence
		if fence.AccountID != source.accountID || fence.ConfigRevision != configRevision {
			return nil, errors.New("J1 source fence 与账户 revision 不一致")
		}
		stateKey, err := requiredFenceText(fence.StateKey, "sourceFence.stateKey")
		if err != nil {
			return nil, err
		}
		sourceGeneration, err := positiveInteger(fence.SourceGeneration, "sourceFence.sourceGeneration")
		if err != nil {
			return nil, err
		}
		sourceFenceID, err := requiredFenceText(fence.SourceFenceID, "sourceFence.sourceFenceId")
		if err != nil {
			return nil, err
		}
		runtimeKey, err := requiredFenceText(fence.RuntimeKey, "sourceFence.runtimeKey")
		if err != nil {
			return nil, err
		}
		probeGeneration, err := positiveInteger(fence.ProbeGeneration, "sourceFence.probeGeneration")
		if err != nil {
			return nil, err
		}
		payload["source_fence"] = map[string]any{
			"state_key":         stateKey,
			"account_id":        source.accountID,
			"source_generation": sourceGeneration,
			"source_fence_id":   sourceFenceID,
			"runtime_key":       runtimeKey,
			"probe_generation":  probeGeneration,
			"config_revision":   configRevision,
		}
	}
	if source.keyModelFence != nil {
		fence := source.keyModelFence
		if fence.DispatchRevision != dispatchRevision || !capabilityHashPattern.MatchString(fence.CapabilityHash) ||
			strings.TrimSpace(fence.KeyFingerprint) == "" || strings.TrimSpace(fence.OwnerID) == "" {
			return nil, errors.New("J1 key-model fence 无效")
		}
		payload["key_model_fence"] = map[string]any{
			"capability_hash":   fence.CapabilityHash,
			"key_fingerprint":   strings.TrimSpace(fence.KeyFingerprint),
			"dispatch_revision": fence.DispatchRevision,
			"owner_id":          strings.TrimSpace(fence.OwnerID),
		}
	}
	return payload, nil
}

var capabilityHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func requiredFenceText(value, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("J1 fence 缺少 %s", name)
	}
	return normalized, nil
}

func positiveInteger(value int64, name string) (int64, error) {
	if value < 1 {
		return 0, fmt.Errorf("J1 %s 必须是正整数", name)
	}
	return value, nil
}

// SignedHealthEnvelope 与 Node AccountHealthJobsSignedInput / Go
// accounthealth.SignedInputEnvelope 同构。
type SignedHealthEnvelope struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// SignAccountHealthPayload 对齐 Node signAccountHealthJobsInput：密钥必须是
// 至少 32 字节的 canonical base64url；签名覆盖
// `hmac-sha256-v1\n<keyId>\n` + payload 原始字节。
func SignAccountHealthPayload(payload map[string]any, signingKey, keyID string) ([]byte, error) {
	normalizedKey := strings.TrimSpace(signingKey)
	if normalizedKey == "" {
		return nil, errors.New("account-health input 签名密钥缺失")
	}
	normalizedKeyID := strings.TrimSpace(keyID)
	if normalizedKeyID == "" {
		return nil, errors.New("account-health input 签名 key ID 缺失")
	}
	key, err := base64.RawURLEncoding.DecodeString(normalizedKey)
	if err != nil {
		return nil, errors.New("account-health input 签名密钥必须是至少 32 字节的 canonical base64url")
	}
	if len(key) < 32 || base64.RawURLEncoding.EncodeToString(key) != strings.TrimRight(normalizedKey, "=") {
		return nil, errors.New("account-health input 签名密钥必须是至少 32 字节的 canonical base64url")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(healthInputSignatureAlgorithm + "\n" + normalizedKeyID + "\n"))
	_, _ = mac.Write(payloadBytes)
	envelope := SignedHealthEnvelope{
		Algorithm: healthInputSignatureAlgorithm,
		KeyID:     normalizedKeyID,
		Payload:   base64.RawURLEncoding.EncodeToString(payloadBytes),
		Signature: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}
	return json.Marshal(envelope)
}

// AccountHealthJobsRequestPath 对齐 Node accountHealthJobsRequestPath：
// 文件名是 sha256(requestId) 定界 opaque locator。
func AccountHealthJobsRequestPath(root, requestID string) (string, error) {
	normalizedRoot := strings.TrimSpace(root)
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRoot == "" {
		return "", errors.New("account-health request 根目录缺失")
	}
	if normalizedRequestID == "" {
		return "", errors.New("account-health request ID 缺失")
	}
	sum := sha256.Sum256([]byte(normalizedRequestID))
	return filepath.Join(filepath.Clean(normalizedRoot), hex.EncodeToString(sum[:])+healthRequestFileSuffix), nil
}

// publishAccountHealthJobsRequest 对齐 Node publishSignedAccountHealthJobsFile：
// 临时文件 + fsync + 原子 rename，失败清理临时文件。
func publishAccountHealthJobsRequest(root, requestID string, payload map[string]any, signingKey string) (string, error) {
	target, err := AccountHealthJobsRequestPath(root, requestID)
	if err != nil {
		return "", err
	}
	bytes, err := SignAccountHealthPayload(payload, signingKey, healthInputSignatureKeyID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	temporary := target + "." + newUUID() + ".tmp"
	published := false
	defer func() {
		if !published {
			_ = os.Remove(temporary)
		}
	}()
	if err := os.WriteFile(temporary, bytes, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, target); err != nil {
		return "", err
	}
	published = true
	return target, nil
}

// newUUID 生成 RFC4122 v4 形态随机 UUID（与 Node randomUUID 字节形态一致）。
func newUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
