package accountbalance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// BusinessDueAdvancer 是周期余额刷新已结算 outcome（成功、失败、unsupported）
// 后推进业务库 due 游标的持久化边界。Runner 只依赖该接口，不直接触碰
// juhe_business；测试以 fake 记录断言。
type BusinessDueAdvancer interface {
	// AdvancePeriodicDue 把账户的 balance_query_next_refresh_at 从 expected
	// 推进到 next。expected 是候选冻结时读到的 due 值（recovery 候选为 nil，
	// 对应 IS NULL fence）。返回值表示 fence 是否命中；未命中（例如期间被
	// 手动刷新或配置修改抢先推进）不是错误。
	AdvancePeriodicDue(ctx context.Context, accountID string, configRevision int64, expected, next *time.Time) (bool, error)
}

// BusinessDueStore 基于 direct input 同源的业务库连接推进 due 游标。它必须
// 使用业务库连接池（Store 的 juhe_jobs 连接不保证能访问 juhe_business
// schema），因此与 AppendOutcome 是同进程内先后两个独立短事务，无法合并。
type BusinessDueStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewBusinessDueStore(db *sql.DB, now func() time.Time) (*BusinessDueStore, error) {
	if db == nil {
		return nil, errors.New("J2 business due store 缺少业务库连接")
	}
	if now == nil {
		now = time.Now
	}
	return &BusinessDueStore{db: db, now: now}, nil
}

// Close 关闭持有的业务库连接。Service 场景该连接与 direct input 池同源，
// database/sql 的 Close 幂等，双重关闭安全。
func (s *BusinessDueStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// balanceDueText 把 due 写入值规范为毫秒精度 UTC RFC3339 文本。该列是 text，
// 读侧 PostgresDirectInputReader 只接受 RFC3339；毫秒截断保证该值下轮读回后
// 仍能与 timestamptz fence 精确相等（亚微秒在 PG 舍入与驱动截断间可能差 1μs）。
func balanceDueText(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
}

func (s *BusinessDueStore) AdvancePeriodicDue(ctx context.Context, accountID string, configRevision int64, expected, next *time.Time) (bool, error) {
	if strings.TrimSpace(accountID) == "" || configRevision < 1 {
		return false, errors.New("J2 due 推进缺少账户或 config revision")
	}
	if next == nil {
		return false, errors.New("J2 due 推进缺少下一轮刷新时间")
	}
	// 写 balance_query_next_refresh_at 会命中 accounts 上的语句级触发器并置
	// availability dirty 标记，这是依赖该列做失效检测的设计内行为。
	fence := `AND balance_query_next_refresh_at IS NULL`
	args := []any{balanceDueText(*next), balanceDueText(s.now()), accountID, configRevision}
	if expected != nil {
		fence = `AND balance_query_next_refresh_at::timestamptz = $5`
		args = append(args, expected.UTC())
	}
	query := `UPDATE juhe_business.accounts
SET balance_query_next_refresh_at = $1, updated_at = $2
WHERE id = $3 AND config_revision = $4
  AND status = 'active' AND schedulable = 1 AND type = 'api_key'
  AND balance_query_enabled = 1
  AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL
  ` + fence
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed > 0, nil
}
