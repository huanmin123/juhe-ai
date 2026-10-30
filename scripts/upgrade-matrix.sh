#!/usr/bin/env bash
# 升级矩阵本地脚本（升级兼容与迁移账本设计 §15/§17 P4）：对每个指定 tag——
# 归档该 tag 的 backend-go 树 → 临时构建该版 maintenance 二进制 → pgtestkit
# 隔离空库执行该版 --ensure-schema --seed（真实旧版 DDL/种子）→ 运行 HEAD 的
# upgrade 全量编排 → 断言 upgraded/账本 12 行 applied/状态版本 → 重跑短路。
#
# 用法：
#   JUHE_AI_TEST_EXTERNAL=1 ./scripts/upgrade-matrix.sh v1.0.0[,v1.1.0,...]
# 可选：JUHE_AI_UPGRADE_MATRIX_MOCKDATA=1（夹具库追加 mockdata 造数）。
#
# 依赖：本机 Go + git；真库经 pgtestkit 管理连接（JUHE_AI_TEST_PG_ADMIN_URL
# 或 shared.env 自动解析）。tag 不足 3 个时对现有 tag 全量执行（§16.3 冷启动
# 口径）；CI 化属独立裁决（§20.6）。
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

TAGS="${1:-}"
if [ -z "$TAGS" ]; then
  echo "用法: $0 <tag1[,tag2,...]>" >&2
  echo "  例: $0 v1.0.0,v1.1.0,v1.2.0   （对每个 tag：旧版夹具 → HEAD upgrade → 断言）" >&2
  exit 2
fi
if [ "${JUHE_AI_TEST_EXTERNAL:-0}" != "1" ]; then
  echo "[FAIL] 矩阵需要真库：请以 JUHE_AI_TEST_EXTERNAL=1 运行（授权门，pgtestkit 同口径）。" >&2
  exit 2
fi

export JUHE_AI_UPGRADE_MATRIX_TAGS="$TAGS"
if [ "${JUHE_AI_UPGRADE_MATRIX_MOCKDATA:-0}" = "1" ]; then
  export JUHE_AI_UPGRADE_MATRIX_MOCKDATA=1
fi

echo "== 升级矩阵 tags=$TAGS mockdata=${JUHE_AI_UPGRADE_MATRIX_MOCKDATA:-0} =="
cd "$REPO_ROOT/backend-go/projects/maintenance"
exec go test ./cmd/juhe-ai-maintenance/ -run 'TestUpgradeMatrixFromArchivedTags' -count=1 -timeout 30m -v
