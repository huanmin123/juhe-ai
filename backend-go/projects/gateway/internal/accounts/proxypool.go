package accounts

// proxypool.go 是账户写入面（创建/补丁/批量/导入）的代理池绑定与粘性租约
// 助手（代理池设计 §6.1/§8.3/§8.6）。所有写路径共用：
//   - lockProxyPoolForWrite：写账户行之前 FOR UPDATE 锁池行（与 shared
//     proxyresolve.PGLeaseWriter 请求时重分配同一锁序 池→账户→租约，
//     2026-11-06 并发约束：禁止先锁账户再锁池）；
//   - selectProxyPoolMemberForWrite：同一事务内经共享 proxyresolve 选成员
//     （健康读取器传 nil，PG pool_health_status 参与判定；健康全 dead 允许
//     绑定但无租约）；
//   - assignAccountProxyPoolLease / clearAccountProxyPoolLease：租约行
//     INSERT/upsert 与删除，generation 一律取
//     juhe_business.account_proxy_lease_generation_seq（禁止自行 +1）。
//
// 授权实例不写池（§5.2/§8.3）：实例行 proxy_pool_id 不参与解析，写面由调用
// 方拒绝或忽略；本文件的租约键恒为「有效绑定所有者」（普通账户=自身）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// 代理池写入面错误码（§8.6）：常量下沉 accountscore（门面与批量/导入域共
// 用），此处保留门面别名。
const (
	ProxyTargetConflictCode = accountscore.ProxyTargetConflictCode
	ProxyPoolNotFoundCode   = accountscore.ProxyPoolNotFoundCode
	ProxyPoolDisabledCode   = accountscore.ProxyPoolDisabledCode
	ProxyPoolEmptyCode      = accountscore.ProxyPoolEmptyCode
)

// 代理目标互斥（§8.3）：创建/PATCH/批量同时提交非空 proxyProfileId 与
// proxyPoolId 统一 400 proxy_target_conflict。
func proxyTargetConflictError() *ValidationError {
	return &ValidationError{Message: "代理与代理池不能同时选择", Code: ProxyTargetConflictCode}
}

// trimmedProxyTarget 折叠可选指针入参：nil 或空白串一律视为未提交（创建缺省
// /null = 无代理；PATCH null = 清除）。
func trimmedProxyTarget(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(*value)
	return trimmed, trimmed != ""
}

// lockProxyPoolForWrite 在写账户行之前锁池行（FOR UPDATE）。创建/改绑事务
// 必须保持 池→账户→租约 的加锁顺序（shared PGLeaseWriter 请求时重分配同序，
// 避免 deadlock）。
func (s *Store) lockProxyPoolForWrite(ctx context.Context, q queryer, poolID string) error {
	var id string
	var enabled bool
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, enabled FROM `+s.table("proxy_pools")+`
		WHERE id = ?
		FOR UPDATE`), poolID).Scan(&id, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "代理池不存在", Code: ProxyPoolNotFoundCode}
	}
	if err != nil {
		return err
	}
	if !enabled {
		return &ValidationError{Message: "代理池已停用，请选择已启用的代理池", Code: ProxyPoolDisabledCode}
	}
	return nil
}

// selectProxyPoolMemberForWrite 为新绑定/改绑解析租约成员（§6.1）：同一事务
// 内经共享 proxyresolve 选成员（健康 nil → PG pool_health_status）。返回：
//   - (memberID, nil)：已选成员，调用方同事务写租约；
//   - ("", nil)：池有 enabled 成员但健康全 dead 不可分配——允许绑定但无租约
//     （§6.1 健康不可分配不阻止绑定），首次 gateway 请求解析时补租约；
//   - ("", err)：池不存在/停用（ErrPoolUnavailable → §8.6 池错误）或池无
//     enabled 成员（ErrPoolEmpty → proxy_pool_empty）。
func (s *Store) selectProxyPoolMemberForWrite(ctx context.Context, q queryer, poolID string) (string, error) {
	store, err := proxyresolve.NewPostgresStore(q)
	if err != nil {
		return "", err
	}
	resolver, err := proxyresolve.New(proxyresolve.Options{Store: store})
	if err != nil {
		return "", err
	}
	memberID, err := resolver.SelectPoolMember(ctx, poolID)
	if err != nil {
		switch {
		case errors.Is(err, proxyresolve.ErrPoolUnavailable):
			return "", &ValidationError{Message: "代理池不存在或已停用，请选择已启用的代理池", Code: ProxyPoolDisabledCode}
		case errors.Is(err, proxyresolve.ErrPoolEmpty):
			return "", &ValidationError{Message: "代理池没有可用成员，请先添加成员", Code: ProxyPoolEmptyCode}
		}
		return "", err
	}
	return memberID, nil
}

// assignAccountProxyPoolLease 在调用方事务内写入/替换粘性租约（§6.1）：
// 先锁池行（同事务重入为 no-op，保证锁序），再锁账户行并校验配置绑定
// 与租约池一致，最后 upsert 租约——generation 一律取共享序列
// juhe_business.account_proxy_lease_generation_seq，assigned_at/updated_at
// = now()（成员即本次新选，视为换成员一并更新分配时间）。
func (s *Store) assignAccountProxyPoolLease(ctx context.Context, q queryer, accountID, poolID, memberProfileID, reason string) error {
	if err := s.lockProxyPoolForWrite(ctx, q, poolID); err != nil {
		return err
	}
	var configuredPoolID sql.NullString
	err := q.QueryRowContext(ctx, s.bind(`SELECT proxy_pool_id FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL
		FOR UPDATE`), accountID).Scan(&configuredPoolID)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "账户不存在或无权操作"}
	}
	if err != nil {
		return err
	}
	if !configuredPoolID.Valid || configuredPoolID.String != poolID {
		// 配置绑定与租约必须一致（§4.3）：同事务内校验失败即数据竞争，
		// 整体回滚由调用方事务承载。
		return &ValidationError{Message: "账户代理池绑定已变化，请刷新后重试"}
	}
	if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_proxy_leases")+`
		(account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
	VALUES (?, ?, ?, nextval('juhe_business.account_proxy_lease_generation_seq'), ?, now(), now())
	ON CONFLICT (account_id) DO UPDATE SET
		pool_id = EXCLUDED.pool_id,
		proxy_profile_id = EXCLUDED.proxy_profile_id,
		generation = nextval('juhe_business.account_proxy_lease_generation_seq'),
		assignment_reason = EXCLUDED.assignment_reason,
		assigned_at = now(),
		updated_at = now()`),
		accountID, poolID, memberProfileID, reason); err != nil {
		return err
	}
	return nil
}

// clearAccountProxyPoolLease 删除账户租约行（解绑/改绑替换前的旧行；§4.3
// 复合外键要求成员删除前先删租约，删除方向由本入口统一收口）。不存在时
// 静默幂等。
func (s *Store) clearAccountProxyPoolLease(ctx context.Context, q queryer, accountID string) error {
	_, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_proxy_leases")+`
		WHERE account_id = ?`), accountID)
	return err
}

// assertProxyWriteTargetConflict 是写入面统一的「至多一个非空」判定
// （§5.2 两列同时非空是数据损坏，写入路径显式 400）。
func assertProxyWriteTargetConflict(profileTarget, poolTarget string) error {
	if strings.TrimSpace(profileTarget) != "" && strings.TrimSpace(poolTarget) != "" {
		return proxyTargetConflictError()
	}
	return nil
}
