-- postgres_exporter 只读监控角色（应用机 PG 容器内执行一次）
--
-- 用途：postgres-exporter 用这个角色连 juhe_ai 库，只读系统表与业务表，
-- 不复用应用写账号，避免监控侧误写与权限扩散。
--
-- 使用方式（推荐用同目录 install.sh 自动执行；手工执行如下）：
--   cd /opt/juhe-monitoring
--   set -a; . ./.env; set +a
--   docker exec -i juhe-ai-postgres psql -U juhe_ai -d juhe_ai \
--     -v ON_ERROR_STOP=1 -v monitor_password="$MONITORING_PG_PASSWORD" \
--     < sql/monitoring-role.sql
--
-- 幂等：角色已存在时跳过创建；授权语句可重复执行，无副作用。
-- 密码通过 psql 变量传入，不在本文件保存明文。

-- 创建角色（已存在则跳过）。\gexec 执行 format() 生成的语句，
-- %L 负责正确转义密码中的引号与特殊字符。
SELECT format('CREATE ROLE juhe_monitor LOGIN PASSWORD %L', :'monitor_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'juhe_monitor')
\gexec

-- 新实例 PG 版本 >= 14 默认已收回 public schema 的 CREATE；
-- 这里只给连接与只读，不授予任何写权限。
GRANT CONNECT ON DATABASE juhe_ai TO juhe_monitor;

-- 只对实际存在的 schema 授权（不同部署阶段 schema 可能尚未建全）
DO $$
DECLARE s text;
BEGIN
  FOREACH s IN ARRAY ARRAY[
    'juhe_business', 'juhe_stats', 'juhe_usage', 'juhe_dataset',
    'juhe_jobs', 'juhe_j3b'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = s) THEN
      EXECUTE format('GRANT USAGE ON SCHEMA %I TO juhe_monitor', s);
    END IF;
  END LOOP;
END
$$;

-- 系统视图读取（pg_database / pg_stat_user_tables / pg_stat_activity 等）
GRANT pg_monitor TO juhe_monitor;

-- 自定义查询读取的业务表（queries.yaml 只访问这三张）；表不存在时跳过
DO $$
BEGIN
  IF to_regclass('juhe_stats.background_task_runs') IS NOT NULL THEN
    GRANT SELECT ON juhe_stats.background_task_runs TO juhe_monitor;
  END IF;
  IF to_regclass('juhe_stats.stats_job_state') IS NOT NULL THEN
    GRANT SELECT ON juhe_stats.stats_job_state TO juhe_monitor;
  END IF;
  IF to_regclass('juhe_business.accounts') IS NOT NULL THEN
    GRANT SELECT ON juhe_business.accounts TO juhe_monitor;
  END IF;
END
$$;

-- 修改密码：ALTER ROLE juhe_monitor PASSWORD '<新密码>';
-- 新增自定义查询访问其他表时，在这里补对应的 GRANT SELECT。
