package oauthmgmt

// OAuth 刷新跨进程状态端口（计划
// docs/plans/计划-20261106T024618000Z-网关与jobs跨进程共享状态改造.md §B1/§B2）。
//
// 两个窄口都由组合根注入共享 Redis 实现（backend-go-platform/crossprocessstate）：
//   - RefreshLock 让 gateway 手动/401 自动刷新与 jobs 定时 refresh/keepalive
//     对同一账户互斥，避免 rotation 型 refresh token 被并发换发后作废；
//   - CredentialVersionBumper 在 gateway 凭据写事务提交后递增共享版本，供
//     其他 gateway 实例/运行态缓存读取（本实例已有 in-process invalidation）。
//
// 未注入（nil）时保持既有进程内行为：自动刷新单飞/节流与 PG config_revision
// CAS 仍是兜底。生产组合根（cmd/juhe-ai-gateway/compose.go）在共享 Redis
// StateClient 构造后无条件接线；nil 仅用于测试夹具。

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// CrossProcessRefreshLockTTL 与单次换发预算（transport 25s/60s）对齐。
const CrossProcessRefreshLockTTL = 60 * time.Second

// RefreshLock 是账户级刷新互斥的跨进程锁。TryLock 不等待：acquired=false
// 表示其他进程正在刷新同一账户。
type RefreshLock interface {
	TryLock(ctx context.Context, accountID string, ttl time.Duration) (token string, acquired bool, err error)
	Unlock(ctx context.Context, accountID, token string) error
}

// CredentialVersionBumper 是凭据版本投影写入端口：只在 PG 提交成功后调用，
// revision 传提交后的 config_revision（与版本键同值，读者据此检测装载竞态）。
type CredentialVersionBumper interface {
	Bump(ctx context.Context, accountID string, revision int64) error
}

// RefreshLockBusyError 表示跨进程刷新锁被其他进程持有。调用方（401 自动刷新
// 协调器）按“本次跳过”处理，不进入上游失败静默窗；管理面手动刷新返回 409。
type RefreshLockBusyError struct{ Message string }

// Error implements error.
func (e *RefreshLockBusyError) Error() string { return e.Message }

// WithRefreshLock 注入账户级跨进程刷新锁。
func WithRefreshLock(lock RefreshLock) Option {
	return func(s *Store) { s.SetRefreshLock(lock) }
}

// WithCredentialVersionBumper 注入凭据版本投影写入端口。
func WithCredentialVersionBumper(bumper CredentialVersionBumper) Option {
	return func(s *Store) { s.SetCredentialVersionBumper(bumper) }
}

// SetRefreshLock 允许组合根在 Store 构造后接线（oauthStore 先于链运行态
// 服务构造；共享 Redis 客户端由后者提供）。nil 是 no-op。
func (s *Store) SetRefreshLock(lock RefreshLock) {
	if lock != nil {
		s.refreshLock = lock
	}
}

// SetCredentialVersionBumper 允许组合根在 Store 构造后接线。nil 是 no-op。
func (s *Store) SetCredentialVersionBumper(bumper CredentialVersionBumper) {
	if bumper != nil {
		s.credentialBumper = bumper
	}
}

// tryRefreshLock 尝试获取账户级跨进程刷新锁。返回值：
//   - release 非 nil 时必须 defer 调用；
//   - busy=true 表示其他进程持有锁，调用方应跳过本轮；
//   - err 非 nil 表示锁后端不可用，调用方按既有 PG CAS 兜底继续执行（不回退
//     业务，也不把锁故障当刷新失败）。
func (s *Store) tryRefreshLock(ctx context.Context, accountID string) (release func(), busy bool, err error) {
	if s.refreshLock == nil {
		return nil, false, nil
	}
	token, acquired, lockErr := s.refreshLock.TryLock(ctx, accountID, CrossProcessRefreshLockTTL)
	if lockErr != nil {
		return nil, false, lockErr
	}
	if !acquired {
		return nil, true, nil
	}
	release = func() {
		// 请求 ctx 可能已取消；释放锁用 detached context，避免锁占到 TTL。
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if unlockErr := s.refreshLock.Unlock(releaseCtx, accountID, token); unlockErr != nil {
			warnCrossProcess("oauth_refresh_cross_process_unlock_failed", accountID, unlockErr)
		}
	}
	return release, false, nil
}

// bumpCredentialVersion 在 PG 提交成功后把共享版本推进到该次提交的
// config_revision；失败只告警，读者按原有 60s 缓存窗口回退。使用 detached
// ctx：客户端断连不应吞掉已提交轮换的版本投影（最多多花 2s）。
func (s *Store) bumpCredentialVersion(ctx context.Context, accountID string, revision int64) {
	if s.credentialBumper == nil {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.credentialBumper.Bump(bumpCtx, accountID, revision); err != nil {
		warnCrossProcess("oauth_credential_version_bump_failed", accountID, err)
	}
}

// warnCrossProcess 统一跨进程状态故障日志；令牌值绝不入日志。Redis 故障时
// 401 批量请求会反复触发同一条告警，按分钟限流（与 jobs 侧锁告警限流一致）。
var (
	crossProcessWarnMu sync.Mutex
	crossProcessWarnAt time.Time
)

func warnCrossProcess(event, accountID string, err error) {
	crossProcessWarnMu.Lock()
	if time.Since(crossProcessWarnAt) < time.Minute {
		crossProcessWarnMu.Unlock()
		return
	}
	crossProcessWarnAt = time.Now()
	crossProcessWarnMu.Unlock()
	slog.Warn("OAuth 跨进程状态操作失败（刷新/缓存按既有兜底路径继续）",
		"event", event, "accountId", accountID, "error", err)
}
