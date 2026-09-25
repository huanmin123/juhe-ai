package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 账户可用性排期激活的 dispatch revision 家族推进（Node 归档
// account-availability-schedule-status-sync.repository.ts 的
// advanceAccountCircuitDispatchRevisionFamilyIn{Sqlite,}Transaction）。SQL
// 实现与 J1 投影共用 advanceDispatchRevisionFamily（根优先 + 全部授权实例 +
// circuit outbox replay 幂等），此处只提供包外复用入口，供 jobs 组合根把
// oauthrefresh.ActivationHook 装配到 account-availability-schedule-status-sync。

// ScheduleActivationDispatch 在调用方事务内推进账户激活的 dispatch revision
// 家族（transitionId 每次激活新生成，对齐归档 newId('dispatch')）。business
// 只承担方言渲染（table/bind），不得另开连接——tx 必须与状态翻转同一事务，
// 否则自锁。now 缺省取 time.Now。
func ScheduleActivationDispatch(ctx context.Context, business *ProjectionBusinessDB, tx *sql.Tx, accountID string, now func() time.Time) error {
	if business == nil || business.db == nil {
		return errors.New("排期激活 dispatch 推进缺少业务库句柄")
	}
	if tx == nil {
		return errors.New("排期激活 dispatch 推进缺少调用方事务")
	}
	if now == nil {
		now = time.Now
	}
	advancer := &OutcomeProjector{business: business, now: now}
	return advancer.advanceDispatchRevisionFamily(ctx, tx, accountID, newProjectionDispatchTransitionID())
}
