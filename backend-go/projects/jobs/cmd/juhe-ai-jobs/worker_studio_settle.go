package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/jobsched"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/studiosettle"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// wireStudioSettleFamily 把 studio 任务结算 worker（媒体工作室设计 §5）翻转为
// GoWired：studio-tasks-settle。结算仓储在 internal/studiosettle（双模 SQL 直
// 查业务库，jobs 不 import gateway 模块），组合根只做句柄打开、账户水合、出
// 站客户端与对象存储端口的装配。
//
// §5.2 jobs→gateway 通信身份为契约待定案项；本批按任务裁决实现"直接读库 +
// HTTP 调上游"形态：kind=video 按 studio_tasks.upstream_job_id（=media_jobs.id）
// 回读账户亲和三元组（media_jobs 只读）后直连上游轮询/下载（gateway 任务面同
// URL 形态、账户原凭据 Bearer），不经 gateway HTTP、不复制派发循环。
func (a *workerAssembly) wireStudioSettleFamily(_ context.Context) error {
	business, err := openBusinessDB(a, "studio-settle-business")
	if err != nil {
		return err
	}
	objects, err := studiosettle.NewLocalObjectWriter(a.config.ChatAssetsRoot)
	if err != nil {
		_ = business.close()
		return err
	}
	sharedClient, err := upstreamhttp.SharedClient("", upstreamhttp.TransportOptions{})
	if err != nil {
		_ = business.close()
		return fmt.Errorf("创建 studio 结算出站客户端失败: %w", err)
	}
	runtime := &studioSettleRuntime{
		business: business,
		secret:   a.config.Secret,
		logger:   a.logger,
	}
	job := &studiosettle.SettleJob{
		Store:      &studiosettle.TaskStore{DB: business.db, Postgres: business.postgres},
		Accounts:   runtime,
		Upstream:   &studiosettle.UpstreamClient{Doer: sharedClient},
		Objects:    objects,
		Logger:     a.logger,
		LeaseOwner: studioLeaseOwner(a.config.InstanceID),
	}
	a.addCloser(business.close)
	a.scheduleWiredJob("studio-tasks-settle", func(taskCtx context.Context, _ jobsched.TaskContext) (jobsched.TaskResult, error) {
		if _, err := job.Run(taskCtx); err != nil {
			return jobsched.TaskResult{}, err
		}
		return jobsched.TaskResult{}, nil
	})
	return nil
}

// studioLeaseOwner 是任务行租约的领取者身份（实例标识透传；多实例部署按
// JUHE_AI_INSTANCE_ID 区分，租约抢占按 owner + 过期时间裁决）。
func studioLeaseOwner(instanceID string) string {
	if strings.TrimSpace(instanceID) != "" {
		return instanceID
	}
	return "juhe-ai-jobs"
}

// studioSettleRuntime 是 studio 结算 worker 的组合根运行时：账户亲和水合
// （accounts 行 + 凭据封套解密，gateway 任务面 hydrateAccount 同语义：原账户
// 原凭据，永不换账户）。
type studioSettleRuntime struct {
	business *businessDB
	secret   string
	logger   *slog.Logger
}

// Resolve 实现 studiosettle.AccountResolver：按账户 id 水合上游 BaseURL 与
// Bearer 凭据。账户缺席/已删/禁用 → Reachable=false（任务面"任务不可达"语
// 义，jobs 置 expired）；读库/解密失败/凭据缺失 → error（结算失败可重试，
// attempt 上限收口）。
func (r *studioSettleRuntime) Resolve(ctx context.Context, accountID string) (studiosettle.AccountRuntime, error) {
	if strings.TrimSpace(accountID) == "" {
		return studiosettle.AccountRuntime{}, fmt.Errorf("媒体任务账户 id 缺失")
	}
	var (
		accountType          string
		status               string
		deletedAt            string
		credentialsEncrypted string
		protocolCode         string
		protocolVersion      string
	)
	row := r.business.db.QueryRowContext(ctx, r.business.bind(`SELECT type, status,
		COALESCE(deleted_at, ''), COALESCE(credentials_encrypted, ''),
		COALESCE(protocol_code, ''), COALESCE(protocol_version, '')
		FROM `+r.business.table("accounts")+` WHERE id = ?`), accountID)
	if err := row.Scan(&accountType, &status, &deletedAt, &credentialsEncrypted,
		&protocolCode, &protocolVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return studiosettle.AccountRuntime{}, nil
		}
		return studiosettle.AccountRuntime{}, fmt.Errorf("水合 studio 任务账户失败: %w", err)
	}
	if strings.TrimSpace(deletedAt) != "" || status != "active" {
		return studiosettle.AccountRuntime{}, nil
	}
	credentials := map[string]any{}
	if strings.TrimSpace(credentialsEncrypted) != "" {
		var decryptErr error
		credentials, decryptErr = decryptXAIGrokCredentials(r.secret, credentialsEncrypted)
		if decryptErr != nil {
			return studiosettle.AccountRuntime{}, fmt.Errorf("解密 studio 任务账户凭据失败: %w", decryptErr)
		}
	}
	bearer := studioBearerOf(accountType, credentials)
	if bearer == "" {
		return studiosettle.AccountRuntime{}, fmt.Errorf("账户缺少可用的上游凭据：%s", accountID)
	}
	return studiosettle.AccountRuntime{
		Reachable: true,
		BaseURL:   studioBaseURLOf(credentials, protocolCode, protocolVersion),
		Bearer:    bearer,
	}, nil
}

// studioBaseURLOf 镜像 gateway chainBaseURLOf：credentials.base_url 优先，
// 缺失按协议默认（anthropic /v1、gemini 根、其余 openai /v1）。
func studioBaseURLOf(credentials map[string]any, protocolCode, protocolVersion string) string {
	if base, ok := credentials["base_url"].(string); ok && strings.TrimSpace(base) != "" {
		return strings.TrimSpace(base)
	}
	code := strings.ToLower(strings.TrimSpace(protocolCode))
	version := strings.ToLower(strings.TrimSpace(protocolVersion))
	if code == "anthropic" && version == "v1" {
		return "https://api.anthropic.com/v1"
	}
	if code == "gemini" && version == "v1beta" {
		return "https://generativelanguage.googleapis.com"
	}
	return "https://api.openai.com/v1"
}

// studioBearerOf 镜像 gateway chainRuntimeCredentialSource + api-key 轮换池：
// oauth/google_oauth 取 access_token（缺失即凭据不可用——refresh_token 不能
// 直接作轮询凭据，不静默回落）；其余账户类型取凭据池首个 api_key
// （api_keys 轮换列表降级 api_key 单值）。
func studioBearerOf(accountType string, credentials map[string]any) string {
	if accountType == "oauth" || accountType == "google_oauth" {
		if access, ok := credentials["access_token"].(string); ok && strings.TrimSpace(access) != "" {
			return strings.TrimSpace(access)
		}
		return ""
	}
	if raw, ok := credentials["api_keys"].([]any); ok {
		for _, value := range raw {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	if single, ok := credentials["api_key"].(string); ok && strings.TrimSpace(single) != "" {
		return strings.TrimSpace(single)
	}
	return ""
}
