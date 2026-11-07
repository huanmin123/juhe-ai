#!/bin/bash
# 多机滚动发布编排：digest 门禁 → 摘流 → 替换 → 健康门禁 → 接回；
# 任一步失败自动接回并回滚到旧镜像。
# 用法: rolling-release.sh --image repo@sha256:... | --image repo:tag --expected-digest sha256:... \
#   --apply-cmd 'JUHE_AI_GATEWAY_IMAGE=%IMAGE% docker compose -f compose.gateway.yml up -d --no-deps --force-recreate gateway' \
#   --health-url http://127.0.0.1:3306/health \
#   [--drain-cmd '...'] [--attach-cmd '...'] [--container juhe-ai-gateway] \
#   [--drain-wait 3] [--health-timeout 60] [--release-log releases.jsonl]
# 说明：健康门禁要求响应含 "ready":true（gateway `/health` 与 jobs 同款契约）。
# 摘流/接回在集群形态由入口 Caddy 主动健康检查自动完成；--drain-cmd/--attach-cmd
# 留给有独立 LB 或额外摘流手段的部署使用，可省略。
set -euo pipefail
IMAGE=""; EXPECTED_DIGEST=""; APPLY_CMD=""; DRAIN_CMD=""; ATTACH_CMD=""; HEALTH_URL=""
CONTAINER="juhe-ai-gateway"
DRAIN_WAIT=3; HEALTH_TIMEOUT=60; RELEASE_LOG="releases.jsonl"
while [ $# -gt 0 ]; do
  case "$1" in
    --image) IMAGE=$2; shift 2;;
    --expected-digest) EXPECTED_DIGEST=$2; shift 2;;
    --apply-cmd) APPLY_CMD=$2; shift 2;;
    --drain-cmd) DRAIN_CMD=$2; shift 2;;
    --attach-cmd) ATTACH_CMD=$2; shift 2;;
    --container) CONTAINER=$2; shift 2;;
    --health-url) HEALTH_URL=$2; shift 2;;
    --drain-wait) DRAIN_WAIT=$2; shift 2;;
    --health-timeout) HEALTH_TIMEOUT=$2; shift 2;;
    --release-log) RELEASE_LOG=$2; shift 2;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done
for v in IMAGE APPLY_CMD HEALTH_URL; do [ -n "${!v}" ] || { echo "missing --${v}" >&2; exit 2; }; done
# 不可变门禁：image 必须是 digest 引用，或显式提供 --expected-digest 并核对
if [ -z "$EXPECTED_DIGEST" ]; then
  case "$IMAGE" in *@sha256:*) ;; *) echo "image 必须使用 @sha256: digest 引用（或用 --expected-digest 显式钉住）" >&2; exit 3;; esac
fi
# digest 形态前置校验（sha256: + 64 hex），非法直接拒绝，避免 pull 阶段报错掩盖门禁语义
digest_value=${EXPECTED_DIGEST:-${IMAGE##*@}}
case "$digest_value" in
  sha256:*) hex=${digest_value#sha256:};;
  *) echo "digest 必须是 sha256: 开头: $digest_value" >&2; exit 3;;
esac
if ! printf '%s' "$hex" | grep -qE '^[0-9a-f]{64}$'; then
  echo "digest 必须是 64 位小写 hex: $digest_value" >&2; exit 3
fi
pull_ref="$IMAGE"
docker pull -q "$pull_ref" >/dev/null
resolved=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$pull_ref" | head -1)
if [ -n "$EXPECTED_DIGEST" ] && [ "${resolved##*@}" != "$EXPECTED_DIGEST" ]; then
  echo "digest 不匹配: expected=$EXPECTED_DIGEST resolved=$resolved" >&2; exit 4
fi
# 记录回滚点（当前容器镜像）
old_image=$(docker inspect --format '{{.Config.Image}}' "$CONTAINER" 2>/dev/null || true)
ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "{\"time\":\"$ts\",\"image\":\"$IMAGE\",\"digest\":\"$resolved\",\"previous\":\"$old_image\",\"phase\":\"start\"}" >> "$RELEASE_LOG"
echo "[1/5] 摘流: $DRAIN_CMD"
if [ -n "$DRAIN_CMD" ]; then bash -c "$DRAIN_CMD"; fi
sleep "$DRAIN_WAIT"
echo "[2/5] 替换实例"
if ! bash -c "${APPLY_CMD//%IMAGE%/$IMAGE}"; then
  echo "apply 失败，回滚" >&2
  [ -n "$ATTACH_CMD" ] && bash -c "$ATTACH_CMD" || true
  [ -n "$old_image" ] && bash -c "${APPLY_CMD//%IMAGE%/$old_image}" || true
  exit 5
fi
echo "[3/5] 健康门禁: $HEALTH_URL"
deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
healthy=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  body=$(curl -fsS -m 3 "$HEALTH_URL" 2>/dev/null || true)
  if echo "$body" | grep -q '"ready":true'; then healthy=1; break; fi
  sleep 2
done
if [ "$healthy" != "1" ]; then
  echo "健康门禁超时，回滚" >&2
  [ -n "$ATTACH_CMD" ] && bash -c "$ATTACH_CMD" || true
  [ -n "$old_image" ] && bash -c "${APPLY_CMD//%IMAGE%/$old_image}" || true
  exit 6
fi
echo "[4/5] 接回流量"
if [ -n "$ATTACH_CMD" ]; then bash -c "$ATTACH_CMD"; fi
echo "[5/5] 完成"
echo "{\"time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"image\":\"$IMAGE\",\"digest\":\"$resolved\",\"phase\":\"done\"}" >> "$RELEASE_LOG"
