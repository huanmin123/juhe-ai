package auditlog

// 审计日志手动清理 API（POST /__aisys__/api/audit-logs/cleanup，仅管理员）：
// 以当前生效保留配置（DB 行 → env 固化值 → 代码默认的优先级链，与后台自动
// 轮共用 EffectiveRetentionSettings 同一合成函数）立即执行一轮
// CleanupRetention——有界批次 + owner lease 事务内校验，与后台自动轮共用
// 同一 lease 与 SQLite 写锁，天然串行安全。防护参照 tablemonitor cleanup
// 先例：auth.RequireAdmin + kernel.MutationGuardMiddleware（防重放指纹）+
// 操作日志 Sink.Record（契约见 docs/functions/日志与审计设置设计.md）。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// auditCleanupOperationKey 是手动清理的防重放操作键（mutation guard）。
const auditCleanupOperationKey = "auditLogs.cleanup"

// auditCleanupPrefix 与 tablemonitor 路由族一致的系统 API 前缀挂载点。
const auditCleanupPrefix = "/__aisys__/api/audit-logs"

// CleanupStore 是清理路由需要的最小 store 端口（*sqlStore 实现）。
type CleanupStore interface {
	CleanupRetention(context.Context, OwnerLease, RetentionConfig) (RetentionResult, error)
}

// Deps bundles the audit-log cleanup route collaborators.
type Deps struct {
	// Store 执行有界清理批次（lease 事务内校验 + 提交前 fence 复核）。
	Store CleanupStore
	// Lease 提供当前 owner lease（*LeaseKeeper 实现 LeaseSource；每次请求取
	// 当前值，reacquire 换 fence 后不会冻结旧 token——与 producer 同契约）。
	Lease LeaseSource
	// Config 是 env 固化回落配置（LoadConfig 已合并代码默认）。
	Config Config
	// Settings 热读业务库保留设置；nil 时仅用 env 固化值合成。
	Settings *BusinessSettingsReader
	// Auth 提供 RequireAdmin（Mount 的 auth 参数为 nil 时回退到它）。
	Auth *authsys.Deps
	// Sink 记操作日志；nil 保持路由可用不记日志。
	Sink authsys.OperationLogSink
}

// Mount wires the audit-log cleanup route：requireAdmin 家族，mutationGuard
// 包裹 cleanup POST（tablemonitor cleanup 先例的同款防护顺序）。
func (d *Deps) Mount(k *kernel.Kernel, auth *authsys.Deps) {
	if auth == nil {
		auth = d.Auth
	}
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey: auditCleanupOperationKey,
		Actor:        auditCleanupActor,
		Fingerprint:  auditCleanupFingerprint,
	})
	k.Register("POST "+auditCleanupPrefix+"/cleanup", auth.RequireAdmin(guard(http.HandlerFunc(d.cleanupHandler))))
}

// auditCleanupActor 解析 mutation guard 的操作者。
func auditCleanupActor(r *http.Request) string {
	if auth := authsys.AuthContextFrom(r); auth != nil {
		return auth.SystemAccountID
	}
	return "anonymous"
}

// auditCleanupFingerprint 是固定操作指纹（该 POST 无请求体；同 actor 的连击
// 由 guard 默认 TTL 去重，防止确认框双击重复触发清理）。
func auditCleanupFingerprint(r *http.Request) (any, error) {
	return map[string]any{"operation": "audit_logs.cleanup"}, nil
}

// cleanupHandler 执行一轮手动清理并回传 6 项计数；lease 丢失或清理失败返回
// 错误原文（lease 丢失 400，其余 500），不重试。
func (d *Deps) cleanupHandler(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil || d.Lease == nil {
		kernel.WriteError(w, http.StatusInternalServerError, "审计日志清理路由未完成装配")
		return
	}
	var settings RetentionSettings
	if d.Settings != nil {
		var err error
		if settings, err = d.Settings.ReadRetentionSettings(r.Context()); err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "读取审计保留设置失败："+err.Error())
			return
		}
	}
	effective := d.Config.EffectiveRetentionSettings(settings)
	lease := d.Lease.Lease()
	if lease.OwnerID == "" || lease.FenceToken <= 0 {
		kernel.WriteBadRequest(w, ErrOwnerLeaseLost.Error())
		return
	}
	result, err := d.Store.CleanupRetention(r.Context(), lease, effective.RetentionConfigAt(time.Now()))
	if err != nil {
		if errors.Is(err, ErrOwnerLeaseLost) {
			kernel.WriteBadRequest(w, err.Error())
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d.Sink != nil {
		d.recordCleanupOperation(r, result)
	}
	kernel.WriteOK(w, result, "")
}

// recordCleanupOperation 记录手动清理操作日志（模块 auditlog / 动作 cleanup /
// 资源 audit_logs；Changes 放 6 项清理计数）。
func (d *Deps) recordCleanupOperation(r *http.Request, result RetentionResult) {
	auth := authsys.AuthContextFrom(r)
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auditCleanupAuthField(auth, func(a *authsys.AuthContext) string { return a.SystemAccountID }),
		ActorUsername:        auditCleanupAuthField(auth, func(a *authsys.AuthContext) string { return a.Username }),
		ActorDisplayName:     auditCleanupAuthField(auth, func(a *authsys.AuthContext) string { return a.DisplayName }),
		ActorRole:            auditCleanupAuthField(auth, func(a *authsys.AuthContext) string { return a.Role }),
		Mode:                 "admin",
		Module:               "auditlog",
		Action:               "cleanup",
		OperationKey:         auditCleanupOperationKey,
		ResourceType:         "audit_logs",
		ResourceID:           "audit_logs",
		ResourceName:         "审计日志",
		Summary:              "手动清理审计日志",
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []authsys.OperationLogChange{
			{Field: "successHotTrimmed", Label: "成功热窗降级行数", After: strconv.FormatInt(result.SuccessHotTrimmed, 10)},
			{Field: "deletedNonPersistedLogs", Label: "删除非持久化日志行数", After: strconv.FormatInt(result.DeletedNonPersistedLogs, 10)},
			{Field: "deletedLogs", Label: "删除审计日志行数", After: strconv.FormatInt(result.DeletedLogs, 10)},
			{Field: "deletedErrorGroups", Label: "删除错误分组行数", After: strconv.FormatInt(result.DeletedErrorGroups, 10)},
			{Field: "deletedPayloadBlobs", Label: "删除正文 blob 数", After: strconv.FormatInt(result.DeletedPayloadBlobs, 10)},
			{Field: "deletedHotSearchFiles", Label: "删除热搜索文件数", After: strconv.FormatInt(result.DeletedHotSearchFiles, 10)},
		},
	}, r)
}

func auditCleanupAuthField(auth *authsys.AuthContext, pick func(*authsys.AuthContext) string) string {
	if auth == nil {
		return ""
	}
	return pick(auth)
}
