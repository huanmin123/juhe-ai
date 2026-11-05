#!/bin/sh
# 监控机栈安装脚本（幂等，root 执行）。
#
# 前置：本目录（stack/）已上传到监控机 /opt/juhe-monitoring-stack/，且已建好
#       SSH 隧道（见 README）或 target 已在 prometheus.yml 中改成实际地址。
# 用法：
#   cp .env.example .env && chmod 600 .env   # 填 GRAFANA_ADMIN_PASSWORD
#   sh install.sh
#
# 脚本只做本地操作：校验配置、起容器、自检。不改防火墙、不开放公网端口。
set -eu

BASE=/opt/juhe-monitoring-stack
cd "$BASE"

if [ ! -f .env ]; then
  echo "[FAIL] 缺少 $BASE/.env：先 cp .env.example .env 并填写 GRAFANA_ADMIN_PASSWORD" >&2
  exit 2
fi

set -a
. ./.env
set +a

if [ -z "${GRAFANA_ADMIN_PASSWORD:-}" ]; then
  echo "[FAIL] .env 中 GRAFANA_ADMIN_PASSWORD 不能为空" >&2
  exit 2
fi

if [ ! -d "$BASE/dashboards" ] || [ -z "$(ls -A "$BASE/dashboards" 2>/dev/null)" ]; then
  echo "[FAIL] dashboards/ 为空：确认 stack/dashboards/*.json 已随目录上传" >&2
  exit 2
fi

echo "== [1/4] 启动监控栈 =="
docker compose up -d

echo "== [2/4] 等待服务就绪 =="
i=0
while [ $i -lt 30 ]; do
  if curl -fsS --max-time 2 http://127.0.0.1:19091/-/ready >/dev/null 2>&1 \
     && curl -fsS --max-time 2 http://127.0.0.1:13100/ready >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done

echo "== [3/4] 自检 =="
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
check prometheus http://127.0.0.1:19091/-/ready
check loki       http://127.0.0.1:13100/ready
check grafana    http://127.0.0.1:3000/api/health
check blackbox   http://127.0.0.1:19115/

echo "== [4/4] 应用抓取目标状态 =="
if command -v python3 >/dev/null 2>&1; then
  python3 - <<'PY'
import json, urllib.parse, urllib.request
q = urllib.parse.quote('up{job=~"juhe-ai-(gateway|jobs)"}')
try:
    with urllib.request.urlopen("http://127.0.0.1:19091/api/v1/query?query=" + q, timeout=5) as r:
        data = json.load(r)
    for item in data.get("data", {}).get("result", []):
        print("  %s = %s" % (item["metric"].get("job"), item["value"][1]))
    if not data.get("data", {}).get("result"):
        print("  [WARN] 尚未发现 juhe-ai 应用目标：检查 SSH 隧道与 targets")
except Exception as e:
    print("  [WARN] 查询 Prometheus 失败：%s" % e)
PY
fi

echo ""
echo "Grafana: http://127.0.0.1:3000 （账号见 .env；Dashboards → juhe-ai）"
exit "$fail"
