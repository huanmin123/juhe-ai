#!/bin/bash
# 集群模式部署自检（按角色在本机执行；不依赖 SSH，不修改任何状态）。
# 用法：
#   ./verify-cluster.sh --role node    # gateway 节点
#   ./verify-cluster.sh --role jobs    # jobs 节点（leader/standby 状态说明）
#   ./verify-cluster.sh --role edge    # 入口机（Caddy 上游健康 + 全链路）
#   ./verify-cluster.sh                # 当前机器上存在的角色全查
# 退出码：0 全绿，1 存在失败项，2 参数/环境错误。
set -uo pipefail
SELF=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
DIR=$(cd "$(dirname "$0")" && pwd)
cd "$DIR"
ROLE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --role) ROLE=$2; shift 2;;
    -h|--help) sed -n '2,10p' "$SELF"; exit 0;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done
[ -f .env ] || { echo "[FAIL] 缺少 $DIR/.env（cp .env.example .env 并填值）" >&2; exit 2; }

envval() { sed -n "s/^$1=//p" .env | tail -1 | tr -d '\r'; }
FAILED=0
ok()   { echo "  [OK]   $*"; }
warn() { echo "  [WARN] $*"; }
bad()  { echo "  [FAIL] $*"; FAILED=1; }

has_service() { docker compose -f "$1" ps -q "$2" 2>/dev/null | grep -q .; }
container_health() {
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$1" 2>/dev/null || echo missing
}
http_code() { curl -sS -m 5 -o /dev/null -w '%{http_code}' "$@" 2>/dev/null || echo 000; }

check_dirs() { # $1=compose file $2=service
  local compose=$1 service=$2 uid
  uid=$(docker compose -f "$compose" exec -T "$service" id -u 2>/dev/null | tr -d '\r')
  if [ "$uid" = "1000" ]; then ok "$service 容器内运行用户 uid=1000"; else bad "$service 容器内 uid=${uid:-未知}（要求 1000，镜像固定运行用户契约）"; fi
  if docker compose -f "$compose" exec -T "$service" sh -c 'touch data/.verify-$$ && rm data/.verify-$$ && touch logs/.verify-$$ && rm logs/.verify-$$' 2>/dev/null; then
    ok "$service data/ 与 logs/ 可写"
  else
    bad "$service 无法写 data/ 或 logs/（检查挂载、UID/GID 与目录权限）"
  fi
}

check_node() {
  echo "== gateway 节点 =="
  local compose=compose.gateway.yml instance image
  instance=$(envval JUHE_AI_INSTANCE_ID)
  image=$(envval JUHE_AI_GATEWAY_IMAGE)
  [ -n "$instance" ] || { bad "JUHE_AI_INSTANCE_ID 未配置"; return; }
  [ -n "$image" ] || bad "JUHE_AI_GATEWAY_IMAGE 未配置"
  has_service "$compose" gateway || { bad "gateway 容器未运行（docker compose -f $compose up -d）"; return; }
  local health; health=$(container_health juhe-ai-gateway)
  [ "$health" = "healthy" ] && ok "gateway 容器健康状态 healthy" || bad "gateway 容器健康状态 $health"
  docker inspect --format '{{.Config.Image}}' juhe-ai-gateway 2>/dev/null | grep -q '@sha256:' \
    && ok "运行镜像为 digest 引用（不可变发布锚点）" \
    || warn "运行镜像不是 digest 引用：$(docker inspect --format '{{.Config.Image}}' juhe-ai-gateway 2>/dev/null)"
  local body; body=$(curl -fsS -m 5 http://127.0.0.1:3306/health 2>/dev/null || true)
  echo "$body" | grep -q '"ready":true' && ok "/health ready=true" || bad "/health 未就绪：${body:-无响应}"
  local metrics; metrics=$(curl -fsS -m 5 http://127.0.0.1:3306/__aisys__/metrics 2>/dev/null || true)
  echo "$metrics" | grep -q 'juhe_ai_build_info' && ok "build_info 指标可读" || bad "build_info 指标缺失（/__aisys__/metrics）"
  echo "$metrics" | grep -q "instance_id=\"$instance\"" \
    && ok "build_info instance_id=$instance 与配置一致" \
    || bad "build_info instance_id 与 JUHE_AI_INSTANCE_ID=$instance 不一致"
  check_dirs "$compose" gateway
  docker compose -f "$compose" exec -T gateway sh -c 'ls logs 2>/dev/null' 2>/dev/null | grep -q "$instance" \
    && ok "本实例运行日志文件已在 logs/ 落盘" \
    || warn "logs/ 未见含实例名 $instance 的日志文件（启动初期或日志目录契约异常）"
}

check_jobs() {
  echo "== jobs 节点 =="
  local compose=compose.jobs.yml instance image
  instance=$(envval JUHE_AI_JOBS_INSTANCE_ID); [ -n "$instance" ] || instance=juhe-ai-jobs
  image=$(envval JUHE_AI_JOBS_IMAGE)
  [ -n "$image" ] || bad "JUHE_AI_JOBS_IMAGE 未配置"
  has_service "$compose" jobs || { bad "jobs 容器未运行（docker compose -f $compose up -d）"; return; }
  local health; health=$(container_health juhe-ai-jobs)
  [ "$health" = "healthy" ] && ok "jobs 容器健康状态 healthy" || bad "jobs 容器健康状态 $health"
  local body; body=$(curl -fsS -m 5 http://127.0.0.1:3305/health 2>/dev/null || true)
  if echo "$body" | grep -q '"leader":true'; then
    ok "jobs 持有全局 leader（消费任务）"
  elif echo "$body" | grep -q '"leader":false'; then
    warn "jobs 为 standby（正常：全集群应恰有一台 leader；若所有节点都 false 需查 PG 租约）"
  else
    bad "jobs /health 无 leader 字段：${body:-无响应}"
  fi
  check_dirs "$compose" jobs
  docker compose -f "$compose" exec -T jobs sh -c 'ls logs 2>/dev/null' 2>/dev/null | grep -q "$instance" \
    && ok "本实例运行日志文件已在 logs/ 落盘" \
    || warn "logs/ 未见含实例名 $instance 的日志文件（启动初期或日志目录契约异常）"
}

check_edge() {
  echo "== 入口机 =="
  local compose=compose.edge.yml domain upstreams expected
  domain=$(envval JUHE_AI_EDGE_DOMAIN)
  upstreams=$(envval JUHE_AI_GATEWAY_UPSTREAMS)
  [ -n "$domain" ] || { bad "JUHE_AI_EDGE_DOMAIN 未配置"; return; }
  [ -n "$upstreams" ] || { bad "JUHE_AI_GATEWAY_UPSTREAMS 未配置"; return; }
  has_service "$compose" frontend && has_service "$compose" edge \
    || { bad "frontend/edge 容器未同时运行（docker compose -f $compose up -d）"; return; }
  [ "$(container_health juhe-ai-go-frontend)" = "healthy" ] && ok "frontend 容器 healthy" || bad "frontend 容器状态 $(container_health juhe-ai-go-frontend)"
  [ "$(container_health juhe-ai-edge)" = "healthy" ] && ok "edge(Caddy) 容器 healthy" || bad "edge 容器状态 $(container_health juhe-ai-edge)"

  # Caddy 管理面（容器内 localhost:2019）的上游健康快照。
  local snapshot
  snapshot=$(docker compose -f "$compose" exec -T edge wget -qO- http://127.0.0.1:2019/reverse_proxy/upstreams 2>/dev/null || true)
  if [ -z "$snapshot" ]; then
    bad "无法读取 Caddy 上游健康快照（admin API /reverse_proxy/upstreams）"
  else
    local addr block
    for addr in $upstreams; do
      block=$(printf '%s' "$snapshot" | tr '{' '\n' | grep -F "\"address\":\"$addr\"")
      if [ -n "$block" ] && printf '%s' "$block" | grep -q '"healthy":true'; then
        ok "上游 $addr healthy"
      else
        bad "上游 $addr 未出现在健康池或状态非 healthy"
      fi
    done
  fi

  # 全链路：健康、SPA、路由面归属（401 证明请求到达 gateway 而不是前端 404）。
  local code body
  body=$(curl -fsS -m 8 "https://$domain/__aisys__/health" 2>/dev/null || true)
  echo "$body" | grep -q '"ready":true' && ok "HTTPS /__aisys__/health ready=true" || bad "HTTPS /__aisys__/health 未就绪：${body:-无响应}"
  code=$(http_code "https://$domain/__aisys__/index.html")
  [ "$code" = "200" ] && ok "管理面 index.html 200" || bad "管理面 index.html 返回 $code"
  curl -sS -m 8 -D - -o /dev/null "https://$domain/__aisys__/index.html" 2>/dev/null | grep -qi '^cache-control:.*no-store' \
    && ok "index.html Cache-Control: no-store" || warn "index.html 未带 no-store（发布后浏览器可能缓存旧壳）"
  code=$(http_code "https://$domain/v1/models")
  [ "$code" = "401" ] && ok "/v1/models 路由到 gateway（401 鉴权）" || bad "/v1/models 返回 $code（期望 401；404 说明路由落到前端）"
  code=$(http_code "https://$domain/__aipublic__/provider/list")
  [ "$code" = "401" ] && ok "/__aipublic__ 路由到 gateway（401 鉴权）" || bad "/__aipublic__ 返回 $code（期望 401；404 说明路由落到前端）"
}

case "$ROLE" in
  node|gateway) check_node ;;
  jobs) check_jobs ;;
  edge) check_edge ;;
  "")   # 未指定角色：检查本机存在的角色。
    has_service compose.gateway.yml gateway && check_node
    has_service compose.jobs.yml jobs && check_jobs
    has_service compose.edge.yml edge && check_edge
    [ -f compose.gateway.yml ] || [ -f compose.jobs.yml ] || [ -f compose.edge.yml ] || { echo "[FAIL] 当前目录不是集群部署目录" >&2; exit 2; }
    ;;
  *) echo "unknown role: $ROLE（可用 node|jobs|edge）" >&2; exit 2;;
esac

if [ "$FAILED" = "0" ]; then
  echo "== 自检通过 =="
  exit 0
fi
echo "== 自检存在失败项 ==" >&2
exit 1
