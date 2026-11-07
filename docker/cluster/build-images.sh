#!/bin/bash
# 集群模式镜像构建与推送（docker/cluster 专属；单机模式用 docker/single-server/deploy.sh）。
# 产物：<registry>/juhe-ai:<tag>-gateway | -jobs | -maintenance，linux/amd64。
# 用法：
#   ./build-images.sh                       # 读 .env/环境变量，构建并推送三个镜像
#   ./build-images.sh --tag v2026.11.07 --registry registry.example.com/juhe-ai
#   ./build-images.sh --no-push             # 只构建本地镜像（离线/演练）
#   ./build-images.sh --skip-frontend       # 复用 build/frontend-dist（已是当前 HEAD 产物）
# 结束后打印可传给 rolling-release.sh 的 digest 引用，并写入 build/images.txt。
set -euo pipefail
SELF=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DIR=$ROOT/docker/cluster
cd "$ROOT"

TAG=${JUHE_AI_IMAGE_TAG:-}
REGISTRY=${JUHE_AI_REGISTRY:-}
PUSH=1
REBUILD_FRONTEND=1
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) TAG=$2; shift 2;;
    --registry) REGISTRY=$2; shift 2;;
    --no-push) PUSH=0; shift;;
    --skip-frontend) REBUILD_FRONTEND=0; shift;;
    -h|--help) sed -n '2,12p' "$SELF"; exit 0;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done

# .env 只用于取编排变量；不 source（避免把含空格/特殊字符的口令当 shell 代码执行）。
if [ -f "$DIR/.env" ]; then
  [ -n "$TAG" ] || TAG=$(sed -n 's/^JUHE_AI_IMAGE_TAG=//p' "$DIR/.env" | tail -1 | tr -d '\r')
  [ -n "$REGISTRY" ] || REGISTRY=$(sed -n 's/^JUHE_AI_REGISTRY=//p' "$DIR/.env" | tail -1 | tr -d '\r')
fi
[ -n "$REGISTRY" ] || { echo "[FAIL] 缺少 JUHE_AI_REGISTRY（--registry 或 docker/cluster/.env）" >&2; exit 2; }
REGISTRY=${REGISTRY%/}

COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
if [ -z "$TAG" ]; then
  if [ -n "$(git status --porcelain)" ]; then TAG="dev-${COMMIT}-dirty"; else TAG="dev-${COMMIT}"; fi
fi
# Docker tag 合法字符集前置校验（与单机 deploy.sh 同口径）。
if [[ ! "$TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  echo "[FAIL] 版本 tag \"$TAG\" 非法（只允许字母/数字/下划线/点/短横线，且不能以点或短横线开头）" >&2
  exit 2
fi

XP=github.com/huanminabc/juhe-ai/backend-go-platform/version
GO_LDFLAGS="-s -w -X ${XP}.Version=${TAG} -X ${XP}.Commit=${COMMIT} -X ${XP}.BuildTime=${BUILD_TIME}"

echo "== 构建身份 version=$TAG commit=$COMMIT platform=linux/amd64 =="
mkdir -p "$DIR/build/bin"
for p in gateway jobs maintenance; do
  echo "  go build juhe-ai-$p"
  (cd "backend-go/projects/$p" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "$GO_LDFLAGS" -o "$DIR/build/bin/juhe-ai-$p" "./cmd/juhe-ai-$p")
done

if [ "$REBUILD_FRONTEND" = "1" ]; then
  echo "== 构建前端 =="
  if [ ! -x frontend/node_modules/.bin/vite ]; then
    echo "[FAIL] frontend/node_modules 缺失：先在 frontend/ 执行 pnpm install，或传 --skip-frontend 复用既有产物。" >&2
    exit 1
  fi
  (cd frontend && pnpm build)
  DIST_BUILD_ID=$(sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p' frontend/dist/build-info.json)
  HEAD_COMMIT=$(git rev-parse HEAD)
  if [ "$DIST_BUILD_ID" != "$HEAD_COMMIT" ]; then
    echo "[FAIL] 前端产物 buildId=$DIST_BUILD_ID 与 HEAD=$HEAD_COMMIT 不一致，拒绝打包。" >&2
    exit 1
  fi
  rm -rf "$DIR/build/frontend-dist"
  cp -r frontend/dist "$DIR/build/frontend-dist"
else
  # 复用产物必须就是当前 HEAD：否则 edge 会发布与 gateway 不同代的前端。
  if [ ! -f "$DIR/build/frontend-dist/index.html" ]; then
    echo "[FAIL] --skip-frontend 但 $DIR/build/frontend-dist 不存在或无 index.html。" >&2
    exit 1
  fi
  DIST_BUILD_ID=$(sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p' "$DIR/build/frontend-dist/build-info.json")
  HEAD_COMMIT=$(git rev-parse HEAD)
  if [ "$DIST_BUILD_ID" != "$HEAD_COMMIT" ]; then
    echo "[FAIL] 复用产物 buildId=$DIST_BUILD_ID 不是当前 HEAD=$HEAD_COMMIT；去掉 --skip-frontend 重新构建。" >&2
    exit 1
  fi
fi
# frontend-dist 只作为 edge 机部署物分发，不进 gateway/jobs 镜像（区别于单机镜像）。
echo "  前端产物：$DIR/build/frontend-dist（buildId=$(sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p' "$DIR/build/frontend-dist/build-info.json"))"

: > "$DIR/build/images.txt"
for p in gateway jobs maintenance; do
  ref="$REGISTRY/juhe-ai:${TAG}-${p}"
  echo "== docker build $ref =="
  # 二进制固定 linux/amd64：镜像平台必须与二进制一致，不开放 --platform 覆盖。
  docker build --platform linux/amd64 -f "$DIR/Dockerfile.runtime" \
    --build-arg "TARGET_BINARY=juhe-ai-$p" -t "$ref" "$DIR"
  if [ "$PUSH" = "1" ]; then
    docker push "$ref" >/dev/null
    digest=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$ref" \
      | grep -F "$REGISTRY/juhe-ai@sha256:" | head -1 | sed 's/.*@//')
    if [ -z "$digest" ]; then
      echo "[FAIL] $ref 推送后未取到仓库 digest，rolling-release.sh 无法使用该镜像。" >&2
      exit 1
    fi
    echo "$ref@$digest" | tee -a "$DIR/build/images.txt"
  else
    echo "$ref" | tee -a "$DIR/build/images.txt"
  fi
done
echo "== 完成：image 引用见 $DIR/build/images.txt =="
