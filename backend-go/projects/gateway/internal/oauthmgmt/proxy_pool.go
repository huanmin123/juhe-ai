package oauthmgmt

// OAuth 建号 / 刷新 / 重授权的代理池支持（代理池设计 §5.2/§5.3/§6.1/§7.5/§8.3）：
//
//   - 建号（create-from-*、sso-to-oauth）：strict allowedKeys 增 proxyPoolId，
//     与 proxyProfileId 互斥；建号前用池做一次临时成员分配（SelectPoolMember，
//     不落租约），token 交换固定使用该成员 URL；账户落库时由 accounts 写面
//     写入 proxy_pool_id 与租约，本文件再把租约对齐（pin）到临时分配的同一
//     成员——重新校验该成员仍可分配（锁序 池→账户→成员，与 shared
//     proxyresolve.PGLeaseWriter 请求时重分配同序），校验失败明确失败
//     （不静默换出口/直连）。
//   - 刷新 / 重授权 / 401 自动刷新（RotateAccountTokensFromStored）：统一走
//     有效目标解析（oauthProxyRequestURL → proxyresolve 账户绑定语义，含
//     §5.2 授权实例 source 覆盖与租约键 = source account）。池绑定账户使用
//     已固化租约成员；本包不注入 LeaseWriter（只读进程），租约缺失/成员不可
//     用即明确失败，不在 token 刷新中途换 IP、不直连。
//
// 解析一律复用 shared/platform/proxyresolve，不在本包复制解析器。池成员健康
// 走 PG pool_health_status 投影（组合根未向本 Store 注入 Redis 健康读取器；
// §5.3 规则 4 的「缺失/失败回退 PG」语义，Redis dead 投影由 gateway 请求解析
// 路径消费）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// oauthProxyPasswordDecrypter 用 Store 的 runtime secret 解封
// proxy_profiles.password_encrypted（v1 AES-GCM {"password": "..."} envelope，
// 与 decryptJSON 同一体系），供 proxyresolve.BuildProfileURL 注入。
type oauthProxyPasswordDecrypter struct {
	secret string
}

// trimmedOptionalPointer 折叠可选指针入参：nil 或空白串一律视为未提交
// （创建缺省/null = 未绑定）。第二返回值报告「提交了非空值」。
func trimmedOptionalPointer(value *string) (string, bool) {
	if value == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(*value)
	return trimmed, trimmed != ""
}

// DecryptProxyPassword implements proxyresolve.PasswordDecrypter.
func (d oauthProxyPasswordDecrypter) DecryptProxyPassword(_ context.Context, encrypted string) (string, error) {
	fields := map[string]any{}
	if err := decryptJSON(d.secret, encrypted, &fields); err != nil {
		return "", err
	}
	plain, _ := fields["password"].(string)
	if strings.TrimSpace(plain) == "" {
		return "", errors.New("代理密码缺失")
	}
	return plain, nil
}

// proxyResolveStack 是 Store 级共享的 proxyresolve 只读栈（PG store +
// 密码解封器；Health/Writer 缺省）。懒构造 + once：与 sessions 等字段一致，
// Store 构造后不变。
type proxyResolveStack struct {
	once     sync.Once
	resolver *proxyresolve.Resolver
	err      error
}

func (s *Store) proxyResolver() (*proxyresolve.Resolver, error) {
	s.proxyStack.once.Do(func() {
		store, err := proxyresolve.NewPostgresStore(s.db)
		if err != nil {
			s.proxyStack.err = err
			return
		}
		s.proxyStack.resolver, s.proxyStack.err = proxyresolve.New(proxyresolve.Options{
			Store:     store,
			Decrypter: oauthProxyPasswordDecrypter{secret: s.secret},
		})
	})
	return s.proxyStack.resolver, s.proxyStack.err
}

// oauthProxyRequestURL 解析账户 token 请求出站代理 URL（刷新/重授权/自动刷新
// 的统一有效目标解析入口）。overrideProfileID 非空时保持既有 profile 直读
// 语义（建号链 body 显式 proxyProfileId 的对应物）；否则按账户绑定解析：
//
//   - 无绑定 → ""（未绑定代理，直连/环境变量既有行为）；
//   - 直接代理 → 既有 proxyProfileRequestURL（错误文案与状态语义逐字保持）；
//   - 代理池 → 租约成员 URL；本包无 LeaseWriter，租约缺失/池不可用/成员不可
//     分配一律 ValidationError（路由层 400），绝不直连、不重分配。
func (s *Store) oauthProxyRequestURL(ctx context.Context, accountID, overrideProfileID string) (string, error) {
	if override := strings.TrimSpace(overrideProfileID); override != "" {
		return s.proxyProfileRequestURL(ctx, override)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", nil
	}
	resolver, err := s.proxyResolver()
	if err != nil {
		return "", err
	}
	store, err := proxyresolve.NewPostgresStore(s.db)
	if err != nil {
		return "", err
	}
	bindings, err := store.LoadBindings(ctx, []string{accountID})
	if err != nil {
		return "", err
	}
	binding, ok := bindings[accountID]
	if !ok {
		return "", nil
	}
	kind, targetID := binding.Effective()
	switch kind {
	case proxyresolve.BindingNone:
		return "", nil
	case proxyresolve.BindingProfile:
		// 直接代理保持既有 profile 直读路径（错误文案/状态码逐字不变）。
		return s.proxyProfileRequestURL(ctx, targetID)
	case proxyresolve.BindingConflict:
		return "", &ValidationError{Message: "代理与代理池不能同时选择"}
	case proxyresolve.BindingPool:
	default:
		return "", nil
	}
	targets, err := resolver.ResolveBatch(ctx, []string{accountID})
	if err != nil {
		return "", err
	}
	target, ok := targets[accountID]
	if !ok {
		return "", nil
	}
	if !target.Available() {
		message := strings.TrimSpace(target.UnavailableMessage)
		if message == "" {
			message = "账户绑定的代理池当前不可用（停用/无成员/租约不可用）"
		}
		return "", &ValidationError{Message: message}
	}
	if target.Kind == proxyresolve.KindDirect {
		return "", nil
	}
	return target.URL, nil
}

// allocateOAuthPoolMemberURL 建号链的临时成员分配（§6.1 OAuth 建号行）：
// SelectPoolMember 只读选择、不落租约；返回成员档案 ID 与已解密出站 URL，
// token 交换固定使用该 URL。池不存在/停用、无 enabled 成员、无可分配成员
// （含健康全 dead）或成员凭据不可用时返回 ValidationError（路由层 400，
// §8.6 文案），不做直连回退。
func (s *Store) allocateOAuthPoolMemberURL(ctx context.Context, poolID string) (string, string, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return "", "", &ValidationError{Message: "代理池不存在"}
	}
	resolver, err := s.proxyResolver()
	if err != nil {
		return "", "", err
	}
	memberID, err := resolver.SelectPoolMember(ctx, poolID)
	if err != nil {
		switch {
		case errors.Is(err, proxyresolve.ErrPoolUnavailable):
			return "", "", &ValidationError{Message: "代理池不存在或已停用，请选择已启用的代理池"}
		case errors.Is(err, proxyresolve.ErrPoolEmpty):
			return "", "", &ValidationError{Message: "代理池没有可用成员，请先添加成员"}
		}
		return "", "", err
	}
	if strings.TrimSpace(memberID) == "" {
		// SelectPoolMember 对「结构成员存在但健康全 dead」返回 ("", nil)：
		// 绑定语义允许无租约建户（§6.1），但 OAuth 建号必须固定出口完成
		// token 交换，无可分配成员即明确失败（§8.3 OAuth 建号行）。
		return "", "", &ValidationError{Message: "代理池当前没有可分配成员，请稍后重试"}
	}
	store, err := proxyresolve.NewPostgresStore(s.db)
	if err != nil {
		return "", "", err
	}
	profiles, err := store.LoadProfiles(ctx, []string{memberID})
	if err != nil {
		return "", "", err
	}
	profile, ok := profiles[memberID]
	if !ok || !profile.Enabled {
		return "", "", &ValidationError{Message: "代理池成员不可用（不存在或已停用）"}
	}
	memberURL, err := proxyresolve.BuildProfileURL(ctx, profile, oauthProxyPasswordDecrypter{secret: s.secret})
	if err != nil {
		return "", "", &ValidationError{Message: "代理池成员代理凭据不可用"}
	}
	return memberID, memberURL, nil
}

// pinOAuthPoolLease 把建号临时分配的成员固化为账户租约（§6.1 OAuth 建号行：
// 落库时重新校验该成员仍可分配，并把同一成员固化为租约；校验失败明确失败，
// 不静默换出口）。accounts 写面事务（proxy_pool.go 租约写入）可能已按池策略
// 另选成员；本步骤在紧随其后的独立事务里把租约对齐到 token 交换实际使用的
// 成员——锁序与 shared PGLeaseWriter 一致（池 → 账户 → 成员行），generation
// 一律取 juhe_business.account_proxy_lease_generation_seq。
//
// 返回错误（含成员不再可分配）时调用方按「建号失败」处理：明确报错，不静默
// 换出口、不直连。成员仍可分配但租约已是同一成员时幂等成功（不重复推代）。
func (s *Store) pinOAuthPoolLease(ctx context.Context, accountID, poolID, memberProfileID string) error {
	accountID = strings.TrimSpace(accountID)
	poolID = strings.TrimSpace(poolID)
	memberProfileID = strings.TrimSpace(memberProfileID)
	if accountID == "" || poolID == "" || memberProfileID == "" {
		return errors.New("固化 OAuth 池租约缺少账户/池/成员")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启 OAuth 池租约固化事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 锁池行（enabled 复核）：与 shared PGLeaseWriter 同序（池 → 账户）。
	var poolEnabled bool
	err = tx.QueryRowContext(ensureContext(ctx), s.bind(`SELECT enabled FROM `+s.table("proxy_pools")+`
		WHERE id = ?
		FOR UPDATE`), poolID).Scan(&poolEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "代理池不存在，账户池绑定固化失败"}
	}
	if err != nil {
		return fmt.Errorf("锁定代理池行: %w", err)
	}
	if !poolEnabled {
		return &ValidationError{Message: "代理池已停用，账户池绑定固化失败"}
	}

	// 锁账户行并复核：账户必须存在且未删除；池绑定账户不允许同时绑定直接
	// 代理（两列同时非空是数据损坏，§5.2）。
	var ownProfileID sql.NullString
	err = tx.QueryRowContext(ensureContext(ctx), s.bind(`SELECT proxy_profile_id FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL
		FOR UPDATE`), accountID).Scan(&ownProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "账户不存在，账户池绑定固化失败"}
	}
	if err != nil {
		return fmt.Errorf("锁定账户行: %w", err)
	}
	if ownProfileID.Valid && strings.TrimSpace(ownProfileID.String) != "" {
		return &ValidationError{Message: "账户同时绑定代理与代理池（数据损坏），已拒绝固化池租约"}
	}

	// 锁成员行并复核可分配性（§6.2：成员行 enabled + 档案 enabled + 健康非
	// dead + streak < 3；PG 投影，Redis 缺席走 §5.3 回退语义）。
	var memberEnabled, profileEnabled bool
	var health string
	var streak int
	err = tx.QueryRowContext(ensureContext(ctx), s.bind(`SELECT member.enabled, profile.enabled,
		COALESCE(profile.pool_health_status, 'unknown'), profile.pool_health_failure_streak
		FROM `+s.table("proxy_pool_members")+` member
		JOIN `+s.table("proxy_profiles")+` profile ON profile.id = member.proxy_profile_id
		WHERE member.pool_id = ? AND member.proxy_profile_id = ?
		FOR UPDATE OF member, profile`), poolID, memberProfileID).Scan(&memberEnabled, &profileEnabled, &health, &streak)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Message: "临时分配的池成员已不存在，OAuth 建号失败（不换出口）"}
	}
	if err != nil {
		return fmt.Errorf("锁定池成员行: %w", err)
	}
	if !memberEnabled || !profileEnabled || streak >= 3 ||
		proxyresolve.HealthState(strings.TrimSpace(health)) == proxyresolve.HealthDead {
		return &ValidationError{Message: "临时分配的池成员已不可分配，OAuth 建号失败（不换出口）"}
	}

	// 固化同一成员为租约（reason=initial，generation 取共享序列）。账户是
	// 本次新建：租约不该已存在；若并发臂（accounts 写面同事务租约）已写入，
	// upsert 对齐到同一成员，其余字段按本行生效。
	_, err = tx.ExecContext(ensureContext(ctx), s.bind(`INSERT INTO `+s.table("account_proxy_leases")+`
		(account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
	VALUES (?, ?, ?, nextval('juhe_business.account_proxy_lease_generation_seq'), 'initial', now(), now())
	ON CONFLICT (account_id) DO UPDATE SET
		pool_id = EXCLUDED.pool_id,
		proxy_profile_id = EXCLUDED.proxy_profile_id,
		generation = nextval('juhe_business.account_proxy_lease_generation_seq'),
		assignment_reason = 'initial',
		assigned_at = CASE
			WHEN account_proxy_leases.proxy_profile_id IS DISTINCT FROM EXCLUDED.proxy_profile_id THEN now()
			ELSE account_proxy_leases.assigned_at
		END,
		updated_at = now()`), accountID, poolID, memberProfileID)
	if err != nil {
		return fmt.Errorf("固化 OAuth 池租约: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 OAuth 池租约固化: %w", err)
	}
	return nil
}
