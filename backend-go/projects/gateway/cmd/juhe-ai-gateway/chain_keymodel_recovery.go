package main

// PLAN-20261008T113056000Z 根治阶段：key-model 能力熔断记忆（memory 运行态
// 形态）的 gateway 进程内恢复驱动组件。
//
// 形态归属「谁拥有运行态，谁负责恢复」：key-model 运行态在 memory 驱动下是
// gateway 进程内事实（InMemoryKeyModelRuntimeStore，chain_wiring_w2c.go
// selector），恢复驱动因此装配在本进程；redis 驱动下运行态经 Redis 共享，
// 恢复职责归 jobs internal/keymodelrecovery，本组件不装配（避免双 owner
// 重复探针，store 围栏只是兜底而非职责划分）。这是依赖门禁（运行态归属
// 决定），不是功能开关。
//
// runner 契约源：gatewayaccounteffects.KeyModelMemoryRecoveryRunner（Node
// key-model-memory-recovery.ts 移植，扫描节拍 1s/batch 128/并发 32/探针
// 30s）。探针执行链契约源：jobs internal/keymodelrecovery/runner.go 的
// executeProbe（:266-284 账户加载 + fence 过滤）与 defaultProbe（:286-299
// outcome 三分支映射）——逐段对照，不发明语义。
//
// 输入构造契约：exactkeyprobe.DirectInput.ToInput（jobs internal/accounthealth
// direct_input.go 的下沉封套原语）承担 Key 指纹（HMAC-SHA256(secret,key)）、
// v1 凭据封套、base_url 解析、协议 profile 支持面校验——这些契约未导出，
// 直接构造 Input 会复制指纹契约（漂移风险），故按 ToInput 路径实施。
// ToInput 固有的 fail-closed 守卫（账户状态/有效期/冷却 fence/启用绑定）
// 失败即 loader 报错 → unknown 中性，不会误治愈。
//
// 与 jobs J1 输入面的职责边界（设计 §7.6）：本 loader 是恢复探针的窄实现，
// 不做 J1 资格守卫（调度谓词/next_health_check_at/输入版本 epoch）、不做
// 配额过滤（恢复探针场景账户刚在服务流量），只取凭据 + base_url + 健康模型
// 映射 + dispatch revision 围栏。SQL 是 gateway 自己的单账户窄查询（标量子
// 查询替代 jobs 候选分页的 LATERAL/窗口 CTE；双方言先例 proberepo）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedulejitter"
	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

const (
	// 节拍与初始延迟对齐 runner 的 KeyModelRecoveryScanIntervalMs（1s）；
	// 抖动用 shared/platform/schedulejitter（与主链恢复组件同源）。
	chainKeyModelRecoveryInterval     = time.Duration(gatewayaccounteffects.KeyModelRecoveryScanIntervalMs) * time.Millisecond
	chainKeyModelRecoveryInitialDelay = 5 * time.Second
	// 单轮超时覆盖最坏一轮：runner 每 Sweep 的选中数受并发上限 32 收敛
	//（batch 128 只约束 ListDue），32 个探针并发执行且各自受 30s 租约内
	// 硬超时（KeyModelProbeTimeoutMs）→ 理论最坏 ≈ 30s + 结算开销；取 4×
	// 余量 120s，覆盖大 state 表 ListDue/GC 停顿等调度毛刺。pass ctx 派生
	// 自 runCtx，进程退出不被该超时拖延。
	chainKeyModelRecoveryPassTimeout = 120 * time.Second
	// 探针响应体上限对齐 jobs defaultProbe（256 KiB）。
	chainKeyModelRecoveryMaxResponseBytes = int64(256 * 1024)
	// 输入有效期：单轮内即用即弃（探针超时 30s），2 分钟覆盖租约续期窗。
	chainKeyModelRecoveryInputTTL = 2 * time.Minute
	// 与 jobs reader 相同的 TLS policy 快照常量（j1-direct-upstream-v1）。
	chainKeyModelRecoveryTLSPolicy = "j1-direct-upstream-v1"
)

// newChainKeyModelMemoryRecoveryComponent 装配 memory 形态的 key-model 恢复
// 组件。构造失败（loader 配置非法/业务契约表缺失）按组合根 fail-fast 契约
// 向上传播，不在运行期降级。
func newChainKeyModelMemoryRecoveryComponent(store *gatewayaccounteffects.InMemoryKeyModelRuntimeStore, composed *composition, cfg runtimeConfig) (supervisor.Component, error) {
	loader, err := newChainKeyModelProbeInputLoader(composed.db, composed.pgDialect, cfg.Secret)
	if err != nil {
		return supervisor.Component{}, err
	}
	if err := loader.CheckContract(context.Background()); err != nil {
		return supervisor.Component{}, err
	}
	probe := chainKeyModelRecoveryProbe{loader: loader, secret: cfg.Secret}
	runner := gatewayaccounteffects.NewKeyModelMemoryRecoveryRunner(gatewayaccounteffects.KeyModelMemoryRecoveryRunnerOptions{
		Store:  store,
		Probe:  probe.Probe,
		Logger: chainKeyModelRecoveryLogger{},
	})
	return supervisor.Component{
		Name: "key-model-memory-recovery",
		Run: func(runCtx context.Context) error {
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-time.After(chainKeyModelRecoveryInitialDelay + schedulejitter.Offset(chainKeyModelRecoveryInterval)):
			}
			ticker := time.NewTicker(chainKeyModelRecoveryInterval + schedulejitter.Offset(chainKeyModelRecoveryInterval))
			defer ticker.Stop()
			runPass := func() {
				ctx, cancel := context.WithTimeout(runCtx, chainKeyModelRecoveryPassTimeout)
				defer cancel()
				// runner.Sweep 不返回错误（ListDue 失败内部 Warn 按 unknown
				// 处理）；有到期状态时输出一轮摘要便于观察恢复推进。
				result := runner.Sweep(ctx)
				if result.DueCount > 0 {
					slog.Info("key-model 后台恢复扫描完成",
						"event", "gateway_key_model_memory_recovery_swept",
						"due", result.DueCount,
						"started", result.StartedCount,
						"settled", result.SettledCount)
				}
			}
			runPass()
			for {
				select {
				case <-runCtx.Done():
					return runCtx.Err()
				case <-ticker.C:
					runPass()
					// 每轮重置抖动节拍（相邻轮不收敛）。
					ticker.Reset(chainKeyModelRecoveryInterval + schedulejitter.Offset(chainKeyModelRecoveryInterval))
				}
			}
		},
	}, nil
}

// chainKeyModelRecoveryLogger 适配 gatewayaccounteffects.Logger 到进程 slog。
type chainKeyModelRecoveryLogger struct{}

func chainKeyModelRecoveryLogArgs(fields map[string]any) []any {
	args := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		args = append(args, key, value)
	}
	return args
}

func (chainKeyModelRecoveryLogger) Info(fields map[string]any, message string) {
	slog.Info(message, chainKeyModelRecoveryLogArgs(fields)...)
}
func (chainKeyModelRecoveryLogger) Warn(fields map[string]any, message string) {
	slog.Warn(message, chainKeyModelRecoveryLogArgs(fields)...)
}
func (chainKeyModelRecoveryLogger) Error(fields map[string]any, message string) {
	slog.Error(message, chainKeyModelRecoveryLogArgs(fields)...)
}

// chainKeyModelProbeInputSource 是探针适配器消费的最小输入面（生产实现
// chainKeyModelProbeInputLoader；测试以 stub 替换隔离被测逻辑）。
type chainKeyModelProbeInputSource interface {
	LoadAccount(ctx context.Context, accountID string) (exactkeyprobe.Input, error)
}

// chainKeyModelRecoveryProbe 适配窄 InputLoader + 下沉探针执行器为
// gatewayaccounteffects.KeyModelRecoveryProbe（契约源 jobs runner.go
// executeProbe + defaultProbe；加载失败告警节流契约源 warnInputLoadFailed
// runner.go:244-264）。runner 按候选并发起 goroutine 调用 Probe，节流状态
// 因此必须 mutex 保护。
type chainKeyModelRecoveryProbe struct {
	loader chainKeyModelProbeInputSource
	secret string

	// inputLoadWarnMu/inputLoadWarnedAt 支撑 LoadAccount 失败告警按账户 30s
	// 节流（jobs runner.go warnInputLoadFailed 同形：DB 故障 + 多到期状态时
	// 逐次 Warn 可达每秒数十条）。节流命中静默返回（jobs 契约无计数汇总）；
	// map 为 nil（零值构造）时首条计入后正常节流。now 抽出供单测注入 clock。
	inputLoadWarnMu   sync.Mutex
	inputLoadWarnedAt map[string]time.Time
	now               func() time.Time
}

// Probe 对齐 jobs executeProbe：按 state.CredentialSourceAccountID 加载输入，
// 账户加载失败或 fence 不匹配（账户行 dispatch_revision 已变）→ unknown
// 中性；再按 defaultProbe 三分支映射结算。指针接收者：节流状态跨并发调用
// 共享（值接收者会在副本上锁/写，形同虚设）。
func (p *chainKeyModelRecoveryProbe) Probe(input gatewayaccounteffects.KeyModelRecoveryProbeInput) gatewayaccounteffects.KeyModelOutcome {
	ctx := input.Ctx
	if err := ctx.Err(); err != nil {
		return gatewayaccounteffects.KeyModelOutcomeUnknown
	}
	probeInput, err := p.loader.LoadAccount(ctx, input.State.CredentialSourceAccountID)
	if err != nil {
		p.warnInputLoadFailed(input.State.CredentialSourceAccountID, err)
		return gatewayaccounteffects.KeyModelOutcomeUnknown
	}
	if probeInput.AccountID != input.State.CredentialSourceAccountID || probeInput.DispatchRevision != input.State.DispatchRevision {
		return gatewayaccounteffects.KeyModelOutcomeUnknown
	}
	result := exactkeyprobe.ProbeExactKeyModel(ctx, probeInput, input.State.KeyFingerprint, input.State.FinalUpstreamModel, input.State.UpstreamEndpointMode, exactkeyprobe.ProbeOptions{
		Secret:           p.secret,
		Timeout:          time.Duration(gatewayaccounteffects.KeyModelProbeTimeoutMs) * time.Millisecond,
		MaxResponseBytes: chainKeyModelRecoveryMaxResponseBytes,
	})
	return chainKeyModelOutcomeFromProbeResult(result)
}

// warnInputLoadFailed 按账户 30s 节流输出输入加载失败告警（契约源 jobs
// runner.go warnInputLoadFailed runner.go:244-264，逐句对照）：节流命中静默
// 返回（jobs 契约无计数汇总）；零值构造（map 为 nil）时首条计入后正常节流。
func (p *chainKeyModelRecoveryProbe) warnInputLoadFailed(accountID string, err error) {
	const warnInterval = 30 * time.Second
	nowFunc := p.now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()
	p.inputLoadWarnMu.Lock()
	if p.inputLoadWarnedAt == nil {
		p.inputLoadWarnedAt = map[string]time.Time{}
	}
	if last, ok := p.inputLoadWarnedAt[accountID]; ok && now.Sub(last) < warnInterval {
		p.inputLoadWarnMu.Unlock()
		return
	}
	p.inputLoadWarnedAt[accountID] = now
	p.inputLoadWarnMu.Unlock()
	slog.Warn("key-model 恢复探针输入加载失败，按 unknown 处理",
		"event", "gateway_key_model_recovery_input_load_failed",
		"credentialSourceAccountId", accountID,
		"error", err.Error())
}

// chainKeyModelOutcomeFromProbeResult 是 jobs runner.go defaultProbe（:286-299）
// 的三分支 outcome 映射，逐分支对照：complete_success → complete_success，
// probe_task_failure → unknown（任务失败不构成治愈证据也不计入失败），
// 其余（framing_complete_neutral / upstream_failure）→ upstream_not_complete。
func chainKeyModelOutcomeFromProbeResult(result exactkeyprobe.ProbeResult) gatewayaccounteffects.KeyModelOutcome {
	if result.Outcome == exactkeyprobe.OutcomeSuccess {
		return gatewayaccounteffects.KeyModelOutcomeCompleteSuccess
	}
	if result.Outcome == exactkeyprobe.OutcomeTaskFailed {
		return gatewayaccounteffects.KeyModelOutcomeUnknown
	}
	return gatewayaccounteffects.KeyModelOutcomeUpstreamNotComplete
}

// chainKeyModelProbeInputLoader 是 key-model 恢复探针的窄输入读取器：
// 按账户行构造 exactkeyprobe.DirectInput 并经下沉的 ToInput 产出探针输入。
// 双方言（SQLite 业务库 / PostgreSQL juhe_business schema）沿用 proberepo
// 的 table/bind 先例；不做 J1 资格守卫与配额过滤（见文件头职责边界）。
type chainKeyModelProbeInputLoader struct {
	db       *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
}

func newChainKeyModelProbeInputLoader(db *sql.DB, postgres bool, secret string) (*chainKeyModelProbeInputLoader, error) {
	if db == nil {
		return nil, errors.New("key-model 恢复输入读取缺少业务库句柄")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("key-model 恢复输入读取缺少凭据 secret")
	}
	return &chainKeyModelProbeInputLoader{db: db, postgres: postgres, secret: secret, now: func() time.Time { return time.Now().UTC() }}, nil
}

// CheckContract 在装配期做轻量表存在性校验（jobs reader CheckContract 的
// 同形零行读）：业务 schema 未初始化时组合根 fail-fast，而不是每轮探针
// 逐条 Warn。只校验本 loader 实际读取的关系。
func (l *chainKeyModelProbeInputLoader) CheckContract(ctx context.Context) error {
	for _, name := range []string{"accounts", "group_accounts", "account_model_mappings", "proxy_profiles", "resource_authorizations"} {
		if _, err := l.db.ExecContext(ctx, l.bind("SELECT 1 FROM "+l.table(name)+" LIMIT 0")); err != nil {
			return fmt.Errorf("key-model 恢复输入读取缺少业务表 %s: %w", name, err)
		}
	}
	return nil
}

func (l *chainKeyModelProbeInputLoader) table(name string) string {
	if l.postgres {
		return "juhe_business." + name
	}
	return name
}

// bind 把 `?` 占位符改写为 PG 的 $n（proberepo 同款，ISSUE-005 同类）。
func (l *chainKeyModelProbeInputLoader) bind(query string) string {
	if !l.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprintf("%d", index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// chainKeyModelProbeInputSQL 是单账户窄投影（字段语义对照 jobs
// direct_input_reader.go directInputCandidatesSQL 的候选列；binding/mapping
// 的 LATERAL 改写为标量子查询——单行查询无需分页 CTE，双方言原生支持）：
//   - mapping 与 jobs 同谓词（enabled + 健康模型 + 家族匹配 + 实映射），
//     取 updated_at 最新一行；无映射回退 health model 本身（ToInput/
//     ResolveHybridProbeTarget 语义）。
//   - binding 取最新启用绑定（候选键序与 jobs 一致）。
//   - 授权实例账户（delegation）镜像 jobs：凭据/协议/provider 取 source 行。
//   - 不做 deleted 之外的 SQL 资格过滤：状态/有效期/冷却由 ToInput 固有
//     守卫 fail-closed（unknown 中性），调度谓词与配额不做（职责边界）。
const chainKeyModelProbeInputSQL = `
SELECT
  a.id, a.config_revision, a.dispatch_revision, a.provider_code, a.provider_protocol_profile_id, a.protocol_code, a.protocol_version,
  a.type, a.client_compatibility, a.status, a.schedulable,
  a.health_check_endpoint_mode, a.health_check_model,
  (SELECT mm.upstream_model FROM %s mm
    WHERE mm.account_id = (CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE source.id END)
      AND mm.provider_code = (CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.provider_code ELSE source.provider_code END)
      AND mm.enabled = 1 AND mm.source_model = a.health_check_model
      AND (mm.upstream_model <> mm.source_model OR mm.upstream_endpoint_family <> mm.source_endpoint_family)
      AND mm.source_endpoint_family = CASE
        WHEN a.health_check_endpoint_mode IN ('chat_json', 'chat_sse') THEN 'chat_completions'
        WHEN a.health_check_endpoint_mode IN ('responses_json', 'responses_sse') THEN 'responses'
        WHEN a.health_check_endpoint_mode IN ('messages_json', 'messages_sse') THEN 'messages'
        WHEN a.health_check_endpoint_mode = 'generate_content_json' THEN 'generate_content'
        WHEN a.health_check_endpoint_mode = 'generate_content_sse' THEN 'stream_generate_content'
        ELSE 'interactions'
      END
    ORDER BY mm.updated_at DESC, mm.source_model ASC LIMIT 1) AS mapped_upstream_model,
  (SELECT mm.upstream_endpoint_family FROM %s mm
    WHERE mm.account_id = (CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE source.id END)
      AND mm.provider_code = (CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.provider_code ELSE source.provider_code END)
      AND mm.enabled = 1 AND mm.source_model = a.health_check_model
      AND (mm.upstream_model <> mm.source_model OR mm.upstream_endpoint_family <> mm.source_endpoint_family)
      AND mm.source_endpoint_family = CASE
        WHEN a.health_check_endpoint_mode IN ('chat_json', 'chat_sse') THEN 'chat_completions'
        WHEN a.health_check_endpoint_mode IN ('responses_json', 'responses_sse') THEN 'responses'
        WHEN a.health_check_endpoint_mode IN ('messages_json', 'messages_sse') THEN 'messages'
        WHEN a.health_check_endpoint_mode = 'generate_content_json' THEN 'generate_content'
        WHEN a.health_check_endpoint_mode = 'generate_content_sse' THEN 'stream_generate_content'
        ELSE 'interactions'
      END
    ORDER BY mm.updated_at DESC, mm.source_model ASC LIMIT 1) AS mapped_upstream_endpoint_family,
  a.credentials_encrypted, a.account_expires_at, a.cooldown_until, a.temporary_unavailable_continuous_probe_enabled,
  a.cooldown_retest_observation_started_at, a.cooldown_retest_generation,
  source.id, source.config_revision, source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, source.type, source.client_compatibility, source.status, source.schedulable,
  source.account_expires_at, source.cooldown_until, source.last_error_code, source.credentials_encrypted,
  (SELECT ga.group_id FROM %s ga
    WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = 1
      AND (a.authorization_instance_authorization_id IS NULL OR ga.account_authorization_id = a.authorization_instance_authorization_id)
    ORDER BY ga.updated_at DESC, ga.group_id ASC, ga.account_id ASC LIMIT 1) AS binding_group_id,
  (SELECT ga.account_authorization_id FROM %s ga
    WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = 1
      AND (a.authorization_instance_authorization_id IS NULL OR ga.account_authorization_id = a.authorization_instance_authorization_id)
    ORDER BY ga.updated_at DESC, ga.group_id ASC, ga.account_id ASC LIMIT 1) AS binding_account_authorization_id,
  ra.id, ra.status, ra.expires_at,
  proxy.id, proxy.enabled, proxy.type, proxy.host, proxy.port, proxy.username, proxy.password_encrypted
FROM %s a
LEFT JOIN %s source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
LEFT JOIN %s ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN %s proxy ON proxy.id = (CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.proxy_profile_id ELSE source.proxy_profile_id END)
WHERE a.id = ? AND a.deleted_at IS NULL
LIMIT 1`

// LoadAccount 按 ID 读取账户并构造探针输入（消费方：
// chainKeyModelRecoveryProbe，jobs runner executeProbe 同语义——任何错误
// 由消费方落 unknown 中性，不中断恢复扫描）。
func (l *chainKeyModelProbeInputLoader) LoadAccount(ctx context.Context, accountID string) (exactkeyprobe.Input, error) {
	normalized := strings.TrimSpace(accountID)
	if normalized == "" {
		return exactkeyprobe.Input{}, errors.New("key-model 恢复输入读取 account ID 不能为空")
	}
	query := l.bind(fmt.Sprintf(chainKeyModelProbeInputSQL,
		l.table("account_model_mappings"), l.table("account_model_mappings"),
		l.table("group_accounts"), l.table("group_accounts"),
		l.table("accounts"), l.table("accounts"),
		l.table("resource_authorizations"), l.table("proxy_profiles"),
	))

	var (
		id, providerCode, accountType, status        sql.NullString
		configRevision, dispatchRevision             sql.NullInt64
		profile, protocol, protocolVersion           sql.NullString
		clientCompatibility                          sql.NullString
		schedulable                                  any
		endpointMode, healthModel                    sql.NullString
		mappedModel, mappedFamily                    sql.NullString
		credentials                                  sql.NullString
		accountExpires, cooldownUntil                sql.NullString
		continuousProbe                              any
		observationStarted, cooldownGeneration       sql.NullString
		sourceID                                     sql.NullString
		sourceRevision                               sql.NullInt64
		sourceProvider, sourceProfile                sql.NullString
		sourceProtocol, sourceProtocolVersion        sql.NullString
		sourceType, sourceClientCompatibility        sql.NullString
		sourceStatus                                 sql.NullString
		sourceSchedulable                            any
		sourceExpires, sourceCooldown                sql.NullString
		sourceError, sourceCredentials               sql.NullString
		groupID, bindingAuthorizationID              sql.NullString
		authorizationID, authorizationStatus         sql.NullString
		authorizationExpires                         sql.NullString
		proxyID, proxyType, proxyHost, proxyUsername sql.NullString
		proxyPassword                                sql.NullString
		proxyEnabled                                 any
		proxyPort                                    sql.NullInt64
	)
	row := l.db.QueryRowContext(ctx, query, normalized)
	if err := row.Scan(
		&id, &configRevision, &dispatchRevision, &providerCode, &profile, &protocol, &protocolVersion,
		&accountType, &clientCompatibility, &status, &schedulable,
		&endpointMode, &healthModel, &mappedModel, &mappedFamily, &credentials,
		&accountExpires, &cooldownUntil, &continuousProbe,
		&observationStarted, &cooldownGeneration,
		&sourceID, &sourceRevision, &sourceProvider, &sourceProfile, &sourceProtocol, &sourceProtocolVersion, &sourceType, &sourceClientCompatibility, &sourceStatus, &sourceSchedulable,
		&sourceExpires, &sourceCooldown, &sourceError, &sourceCredentials,
		&groupID, &bindingAuthorizationID,
		&authorizationID, &authorizationStatus, &authorizationExpires,
		&proxyID, &proxyEnabled, &proxyType, &proxyHost, &proxyPort, &proxyUsername, &proxyPassword,
	); err != nil {
		return exactkeyprobe.Input{}, fmt.Errorf("读取 key-model 恢复输入账户行失败: %w", err)
	}

	account := exactkeyprobe.DirectAccount{
		ID:                           id.String,
		ConfigRevision:               configRevision.Int64,
		DispatchRevision:             dispatchRevision.Int64,
		Provider:                     providerCode.String,
		ProtocolProfileID:            profile.String,
		ProtocolCode:                 protocol.String,
		ProtocolVersion:              protocolVersion.String,
		Type:                         accountType.String,
		ClientCompatibility:          clientCompatibility.String,
		Status:                       status.String,
		Schedulable:                  truthyAny(schedulable),
		EndpointMode:                 endpointMode.String,
		HealthModel:                  healthModel.String,
		MappedUpstreamModel:          mappedModel.String,
		MappedUpstreamEndpointFamily: mappedFamily.String,
		CredentialsEncrypted:         credentials.String,
		TemporaryUnavailableContinuousProbeEnabled: truthyAny(continuousProbe),
	}
	var err error
	if account.AccountExpiresAt, err = parseNullableInstant(accountExpires); err != nil {
		return exactkeyprobe.Input{}, err
	}
	if account.CooldownUntil, err = parseNullableInstant(cooldownUntil); err != nil {
		return exactkeyprobe.Input{}, err
	}
	if observationStarted.Valid || cooldownGeneration.Valid {
		observed, parseErr := parseNullableInstant(observationStarted)
		if parseErr != nil || observed == nil || !cooldownGeneration.Valid || strings.TrimSpace(cooldownGeneration.String) == "" {
			return exactkeyprobe.Input{}, errors.New("key-model 恢复输入的 cooldown fence 无效")
		}
		account.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: *observed, Generation: cooldownGeneration.String}
	}
	direct := exactkeyprobe.DirectInput{
		Account:      account,
		Binding:      exactkeyprobe.DirectBinding{GroupID: groupID.String, Enabled: groupID.Valid, AuthorizationBindingID: bindingAuthorizationID.String},
		InputVersion: 1,
		IssuedAt:     l.now(),
		ExpiresAt:    l.now().Add(chainKeyModelRecoveryInputTTL),
		TLSPolicy:    chainKeyModelRecoveryTLSPolicy,
		// Schedule 是 J1 周期调度快照；恢复探针执行器（ProbeExactKeyModel）
		// 不消费该快照，零值即最小诚实填充。
		Schedule: exactkeyprobe.Schedule{},
	}
	if authorizationID.Valid {
		expiresAt, parseErr := parseNullableInstant(authorizationExpires)
		if parseErr != nil {
			return exactkeyprobe.Input{}, parseErr
		}
		// QuotaEligible 恒 true：恢复探针不做配额过滤（设计 §7.6 职责边界；
		// 授权状态/有效期仍由 ToInput 固有守卫校验）。
		direct.Authorization = &exactkeyprobe.DirectAuthorization{ID: authorizationID.String, Status: authorizationStatus.String, ExpiresAt: expiresAt, QuotaEligible: true}
	}
	if sourceID.Valid {
		sourceExpiresAt, parseErr := parseNullableInstant(sourceExpires)
		if parseErr != nil {
			return exactkeyprobe.Input{}, parseErr
		}
		sourceCooldownUntil, parseErr := parseNullableInstant(sourceCooldown)
		if parseErr != nil {
			return exactkeyprobe.Input{}, parseErr
		}
		direct.Source = &exactkeyprobe.DirectSource{
			ID:                   sourceID.String,
			ConfigRevision:       sourceRevision.Int64,
			Provider:             sourceProvider.String,
			ProtocolProfileID:    sourceProfile.String,
			ProtocolCode:         sourceProtocol.String,
			ProtocolVersion:      sourceProtocolVersion.String,
			Type:                 sourceType.String,
			ClientCompatibility:  sourceClientCompatibility.String,
			Status:               sourceStatus.String,
			Schedulable:          truthyAny(sourceSchedulable),
			AccountExpiresAt:     sourceExpiresAt,
			CooldownUntil:        sourceCooldownUntil,
			LastErrorCode:        sourceError.String,
			CredentialsEncrypted: sourceCredentials.String,
		}
		if direct.Account.Cooldown != nil {
			value := direct.Source.ConfigRevision
			direct.Account.Cooldown.SourceConfigRevision = &value
		}
	}
	if proxyID.Valid {
		direct.Proxy = &exactkeyprobe.DirectProxy{
			ID:                proxyID.String,
			Enabled:           truthyAny(proxyEnabled),
			Type:              proxyType.String,
			Host:              proxyHost.String,
			Port:              int(proxyPort.Int64),
			Username:          proxyUsername.String,
			PasswordEncrypted: proxyPassword.String,
		}
	}
	now := l.now()
	return direct.ToInput(l.secret, now)
}

// truthyAny 兼容双方言的整型布尔列（SQLite int64 / PG integer；proberepo
// truthy 同款 any 扫描先例）。
func truthyAny(value any) bool {
	switch typed := value.(type) {
	case int64:
		return typed == 1
	case int32:
		return typed == 1
	case float64:
		return typed == 1
	case bool:
		return typed
	case []byte:
		return string(typed) == "1" || string(typed) == "true"
	case string:
		return typed == "1" || typed == "true"
	default:
		return false
	}
}

// parseNullableInstant 解析双方言时间列（SQLite RFC3339 文本 / PG
// timestamptz 经驱动转字符串；proberepo instantMS 同款先例）。
func parseNullableInstant(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.String))
	if err != nil {
		return nil, fmt.Errorf("解析 key-model 恢复输入时间失败: %s", value.String)
	}
	return &parsed, nil
}
