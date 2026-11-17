#!/usr/bin/env bash
# 监控查询漂移检测（#T211，2026-11-17 统计停摆告警盲区收口）。
#
# 背景：queries.yaml 曾出现三副本漂移（repo 已修 / 103 旧 / Windows 权威副本旧），
# 导致 AppJobStalled 在生产上「失败轮次刷新 finished_at」把停摆任务谎报为 0 分钟，
# 统计停摆 6.5h+ 无告警。本脚本把仓库副本确立为**单一权威源**，比对生产 103 与
# 可选本地权威副本的功能性差异（忽略整行 # 注释、整行 -- 注释与空行）。
#
# 用法（巡检或发布前后，本地执行）：
#   bash scripts/check-monitoring-drift.sh
# 可选环境变量：
#   MONITORING_AUTHORITY_FILE  Windows 工作机权威副本路径（存在时一并核对）
#   MONITORING_REMOTE          默认 root@103.36.63.105
#   MONITORING_REMOTE_FILE     默认 /opt/juhe-monitoring/queries.yaml
#
# 退出码：0=一致；1=存在漂移（输出 diff）。漂移时按
# `.local/project-resources/prod/runbooks/监控与告警运维.md` §6 同步后重启 exporter
# 并验证 juhe_app_* 指标仍在输出。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CANON="$ROOT/docker/single-server/monitoring/collector/queries.yaml"
SSH_KEY="$ROOT/.local/project-resources/prod/assets/ssh/juhe_ai_cn103_ed25519"
REMOTE="${MONITORING_REMOTE:-root@103.36.63.105}"
REMOTE_FILE="${MONITORING_REMOTE_FILE:-/opt/juhe-monitoring/queries.yaml}"

if [[ ! -f "$CANON" ]]; then
  echo "[FAIL] 权威副本不存在: $CANON" >&2
  exit 1
fi

# 功能性归一：去空行、整行 # 注释（YAML 注）、整行 -- 注释（SQL 注）。
normalize() {
  awk 'NF && $0 !~ /^[[:space:]]*#/ && $0 !~ /^[[:space:]]*--/' "$1"
}

status=0
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

echo "== 权威源: $CANON"
echo "== 比对目标: $REMOTE:$REMOTE_FILE"
ssh -i "$SSH_KEY" -o BatchMode=yes -o ConnectTimeout=15 "$REMOTE" "cat '$REMOTE_FILE'" >"$tmp"
if diff -u <(normalize "$CANON") <(normalize "$tmp") >/tmp/monitoring-drift.diff 2>&1; then
  echo "[OK] 生产 103 与仓库权威副本功能一致"
else
  echo "[DRIFT] 生产 103 与仓库权威副本存在功能差异："
  cat /tmp/monitoring-drift.diff
  status=1
fi

if [[ -n "${MONITORING_AUTHORITY_FILE:-}" ]]; then
  echo "== 比对本地权威副本: $MONITORING_AUTHORITY_FILE"
  if [[ ! -f "$MONITORING_AUTHORITY_FILE" ]]; then
    echo "[FAIL] 本地权威副本不存在: $MONITORING_AUTHORITY_FILE" >&2
    status=1
  elif diff -u <(normalize "$CANON") <(normalize "$MONITORING_AUTHORITY_FILE") >/tmp/monitoring-drift-authority.diff 2>&1; then
    echo "[OK] 本地权威副本与仓库权威副本功能一致"
  else
    echo "[DRIFT] 本地权威副本与仓库权威副本存在功能差异："
    cat /tmp/monitoring-drift-authority.diff
    status=1
  fi
fi

exit "$status"
