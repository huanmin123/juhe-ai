package schema

import "strings"

// 安装与 revision 33 共用函数体，确保函数与 dirty 表的物理类型一致。
const dirtyAvailabilityFunctionBody = `DECLARE
  v_now {{TYPE}};
  v_prev_lock_timeout text := current_setting('lock_timeout', true);
BEGIN
  IF COALESCE(array_length(p_account_ids, 1), 0) = 0 THEN
    RETURN;
  END IF;
  -- 问题-0192：本函数是全部触发器隐式 dirty 写入的公共汇聚点，与所有应用侧
  -- dirty 写事务（Go advisorylock.AccountListDirty = 7001001）共用 advisory 锁，
  -- 使触发器路径也纳入 dirty 写串行化，消除与批量写事务的锁序死锁（40P01）。
  -- pg_advisory_xact_lock 事务级可重入：已持锁事务经触发器再次取锁无阻塞。
  -- 问题-0313 后续（PLAN-20261017T030349000Z）：仅本次 advisory 等待有界，
  -- 与 Go 侧 advisorylock.ListDirtyLockWaitTimeout 同口径 2s；包裹+恢复使宿主
  -- 事务其余锁等待行为不变（零外溢）。55P03 中止宿主事务——「触发变更提交
  -- ⇔ dirty 行写入」同生共死，不做捕获跳过（捕获=静默漂移，禁止）。
  PERFORM set_config('lock_timeout', '2s', true);
  PERFORM pg_advisory_xact_lock(7001001);
  PERFORM set_config('lock_timeout', COALESCE(v_prev_lock_timeout, '0'), true);
  v_now := {{NOW}};
  WITH requested_accounts AS (
    SELECT DISTINCT requested.account_id
    FROM unnest(p_account_ids) AS requested(account_id)
    WHERE account_id IS NOT NULL AND btrim(account_id) <> ''
  ), affected_accounts AS (
    SELECT DISTINCT accounts.id
    FROM accounts
    INNER JOIN requested_accounts
      ON accounts.id = requested_accounts.account_id
        OR accounts.authorization_instance_source_account_id = requested_accounts.account_id
  )
  INSERT INTO account_list_availability_dirty (
    account_id, viewer_system_account_id, generation, applied_generation, reason,
    available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count,
    created_at_ms, updated_at_ms
  )
  SELECT accounts.id, accounts.system_account_id,
    COALESCE((
      SELECT MAX(projections.source_generation)
      FROM account_list_availability_projections projections
      WHERE projections.account_id = accounts.id
    ), 0) + 1,
    0, left(p_reason, 128), v_now, NULL, NULL, NULL, 0, v_now, v_now
  FROM affected_accounts
  INNER JOIN accounts ON accounts.id = affected_accounts.id
  ON CONFLICT (account_id) DO UPDATE SET
    viewer_system_account_id = excluded.viewer_system_account_id,
    generation = account_list_availability_dirty.generation + 1,
    reason = excluded.reason,
    available_at_ms = LEAST(account_list_availability_dirty.available_at_ms, excluded.available_at_ms),
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    updated_at_ms = excluded.updated_at_ms;
END;`

func dirtyAvailabilityFunctionBodyFor(columnType, now string) string {
	body := strings.ReplaceAll(dirtyAvailabilityFunctionBody, "{{TYPE}}", columnType)
	return strings.ReplaceAll(body, "{{NOW}}", now)
}

func dirtyAvailabilityFunctionSQL(body string) string {
	return `CREATE OR REPLACE FUNCTION juhe_business.account_list_availability_mark_dirty_accounts(
  p_account_ids text[], p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
` + body + `
$function$;`
}

// DirtyAvailabilityFunctionNativeBody 返回 timestamp 函数体，供安装和验证共用。
func DirtyAvailabilityFunctionNativeBody() string {
	return dirtyAvailabilityFunctionBodyFor("timestamptz(6)", "clock_timestamp()")
}

// DirtyAvailabilityFunctionLegacySQL 返回迁移前 bigint 函数的安装语句。
func DirtyAvailabilityFunctionLegacySQL() string {
	return dirtyAvailabilityFunctionSQL(dirtyAvailabilityFunctionBodyFor("bigint", "FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint"))
}

// DirtyAvailabilityFunctionNativeSQL 返回 revision 33 后 timestamp 函数的安装语句。
func DirtyAvailabilityFunctionNativeSQL() string {
	return dirtyAvailabilityFunctionSQL(DirtyAvailabilityFunctionNativeBody())
}

// DirtyAvailabilityFunctionInstallerSQL 在安装时按列类型选函数；运行时不检查类型。
func DirtyAvailabilityFunctionInstallerSQL() string {
	legacy := DirtyAvailabilityFunctionLegacySQL()
	native := DirtyAvailabilityFunctionNativeSQL()
	return `DO $dirty_install$
DECLARE
  v_columns integer;
  v_legacy integer;
  v_native integer;
BEGIN
  SELECT count(*),
         count(*) FILTER (WHERE data_type = 'bigint'),
         count(*) FILTER (WHERE data_type = 'timestamp with time zone' AND datetime_precision = 6)
    INTO v_columns, v_legacy, v_native
    FROM information_schema.columns
   WHERE table_schema = 'juhe_business' AND table_name = 'account_list_availability_dirty'
     AND column_name IN ('available_at_ms', 'claim_until_ms', 'created_at_ms', 'updated_at_ms');
  IF v_columns = 4 AND v_legacy = 4 THEN
    EXECUTE $dirty_ddl$` + legacy + `$dirty_ddl$;
  ELSIF v_columns = 4 AND v_native = 4 THEN
    EXECUTE $dirty_ddl$` + native + `$dirty_ddl$;
  ELSE
    RAISE SQLSTATE '42804' USING MESSAGE = 'account_list_availability_dirty 时间列必须全部为 bigint 或 timestamptz(6)';
  END IF;
END;
$dirty_install$;
`
}
