#!/bin/sh
# 应用机采集端安装脚本（幂等，root 执行）。
#
# 前置：juhe-ai 应用 compose 已运行（项目名 juhe-ai，网络 juhe-ai_default）。
# 用法：
#   1. 把整个 collector/ 目录上传到应用机 /opt/juhe-monitoring/；
#   2. cp .env.example .env && chmod 600 .env，填写 MONITORING_PG_PASSWORD / REDIS_PASSWORD；
#   3. sh install.sh
#
# 脚本只做本地操作：建目录、建 PG 只读角色、起采集容器、装 cron。
# 不改应用 compose，不改防火墙，不开放任何公网端口。
set -eu

BASE=/opt/juhe-monitoring
cd "$BASE"

if [ ! -f .env ]; then
  echo "[FAIL] 缺少 $BASE/.env：先 cp .env.example .env 并填写密码" >&2
  exit 2
fi

set -a
. ./.env
set +a

if [ -z "${MONITORING_PG_PASSWORD:-}" ] || [ -z "${REDIS_PASSWORD:-}" ]; then
  echo "[FAIL] .env 中 MONITORING_PG_PASSWORD / REDIS_PASSWORD 不能为空" >&2
  exit 2
fi

echo "== [1/5] 创建运行目录 =="
mkdir -p "$BASE"/textfile "$BASE"/redis-slowlog "$BASE"/stall-sampler "$BASE"/alloy

echo "== [2/5] 创建 PG 只读角色、授权与监控查询索引 =="
if docker inspect juhe-ai-postgres >/dev/null 2>&1; then
  docker exec -i juhe-ai-postgres psql -U juhe_ai -d juhe_ai \
    -v ON_ERROR_STOP=1 -v monitor_password="$MONITORING_PG_PASSWORD" \
    < "$BASE/sql/monitoring-role.sql"
  # 索引需表 owner 权限，用应用账号执行；缺失会让任务停摆查询退化为全表扫描
  docker exec -i juhe-ai-postgres psql -U juhe_ai -d juhe_ai \
    -v ON_ERROR_STOP=1 < "$BASE/sql/query-indexes.sql"
else
  echo "[WARN] 未找到 juhe-ai-postgres 容器，跳过角色与索引创建；应用栈启动后重跑本脚本" >&2
fi

echo "== [3/5] 启动采集容器 =="
docker compose up -d

echo "== [4/5] 安装定时任务 =="
chmod 0755 "$BASE/redis-slowlog-snapshot.py" "$BASE/stall-sampler.sh"
cp "$BASE/cron/juhe-slowlog-snapshot" "$BASE/cron/juhe-stall-sampler" /etc/cron.d/
chmod 0644 /etc/cron.d/juhe-slowlog-snapshot /etc/cron.d/juhe-stall-sampler
if command -v systemctl >/dev/null 2>&1; then
  systemctl reload cron 2>/dev/null || systemctl reload crond 2>/dev/null || true
fi

echo "== [5/5] 自检 =="
fail=0
check() {
  name=$1; url=$2
  if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
    echo "  [OK]   $name  $url"
  else
    echo "  [FAIL] $name  $url" >&2
    fail=1
  fi
}
check node-exporter  http://127.0.0.1:9100/metrics
check cadvisor       http://127.0.0.1:18081/metrics
check postgres-exp   http://127.0.0.1:9187/metrics
check redis-exp      http://127.0.0.1:9121/metrics
check gateway        http://127.0.0.1:3306/__aisys__/metrics
check jobs           http://127.0.0.1:3305/__aisys__/metrics

echo ""
echo "慢日志快照与僵死采样会在下一个 cron 周期产出；可手动先跑一次："
echo "  $BASE/redis-slowlog-snapshot.py && $BASE/stall-sampler.sh"
echo "  ls -l $BASE/textfile/"

exit "$fail"
