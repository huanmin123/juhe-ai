#!/usr/bin/env bash
# 国内单机生产发布脚本（103.36.63.105，go-only 单机 Docker）。
#
# 防呆设计（对应 2026-09-26 部署事故：shared 模块修复后 gateway 用旧产物上线）：
#   1. 每次发布无条件全量重编译目标二进制（不信任 build/bin 里的既有产物）；
#   2. md5 三点闭环：本地新编译 = 服务器 build/bin = 容器内运行二进制；
#   3. 逐容器等待 healthy 后才判定成功（F3 audit 租约释放期会有 1-2 分钟重启循环，属预期）。
#
# 红线：本机构建（服务器严禁编译，仅 docker compose build 组装镜像），见同目录 README 与
# .local/project-resources/prod/runbooks/国内单机Docker部署与运维.md。
#
# 用法：
#   ./deploy.sh all        发布 gateway + jobs（默认）
#   ./deploy.sh gateway    只发布 gateway
#   ./deploy.sh jobs       只发布 jobs
#   ./deploy.sh maintenance 只发布 maintenance（不 up，等下次 compose run 生效）
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
SERVER=root@103.36.63.105
SERVER_DIR=/opt/juhe-ai
SSH="ssh -i $REPO_ROOT/.local/project-resources/prod/assets/ssh/juhe_ai_cn103_ed25519 -o BatchMode=yes -o ConnectTimeout=15"
HEALTH_URL=https://aijh.huanmin.top/__aisys__/health

TARGET="${1:-all}"
case "$TARGET" in
  all) TARGETS=(gateway jobs) ;;
  gateway|jobs|maintenance) TARGETS=("$TARGET") ;;
  *) echo "用法: $0 [all|gateway|jobs|maintenance]" >&2; exit 1 ;;
esac

cd "$REPO_ROOT"
echo "== [1/5] 全量重编译（linux/amd64，不信任既有产物）=="
BUILD_BIN=docker/single-server/build/bin
mkdir -p "$BUILD_BIN"
declare -A LOCAL_MD5
for p in "${TARGETS[@]}"; do
  (cd "backend-go/projects/$p" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "../../../$BUILD_BIN/juhe-ai-$p" "./cmd/juhe-ai-$p")
  LOCAL_MD5[$p]=$(md5sum "$BUILD_BIN/juhe-ai-$p" | awk '{print $1}')
  echo "  juhe-ai-$p  ${LOCAL_MD5[$p]}"
done

echo "== [2/5] 上传到 $SERVER:$SERVER_DIR/build/bin =="
tar czf - -C "$BUILD_BIN" ${TARGETS[@]/#/juhe-ai-} \
  | $SSH "$SERVER" "tar xzf - -C $SERVER_DIR/build/bin && chmod 0755 $SERVER_DIR/build/bin/juhe-ai-*"

echo "== [3/5] 服务器侧 md5 比对（上传完整性）=="
for p in "${TARGETS[@]}"; do
  SERVER_MD5=$($SSH "$SERVER" "md5sum $SERVER_DIR/build/bin/juhe-ai-$p" | awk '{print $1}')
  if [ "$SERVER_MD5" != "${LOCAL_MD5[$p]}" ]; then
    echo "  [FAIL] juhe-ai-$p 上传后 md5 不一致: 本地 ${LOCAL_MD5[$p]} vs 服务器 $SERVER_MD5" >&2
    exit 1
  fi
  echo "  juhe-ai-$p  OK"
done

echo "== [4/5] 服务器组装镜像并滚动更新 =="
$SSH "$SERVER" "cd $SERVER_DIR && docker compose build ${TARGETS[*]} 2>&1 | tail -1 && docker compose up -d ${TARGETS[*]} 2>&1 | tail -2"

echo "== [5/5] 健康 check + 容器运行二进制闭环校验 =="
for p in "${TARGETS[@]}"; do
  [ "$p" = "maintenance" ] && continue  # maintenance 是一次性 tool 容器，无常驻进程
  ok=""
  for i in $(seq 1 30); do
    st=$($SSH "$SERVER" "cd $SERVER_DIR && docker compose ps $p --format '{{.Status}}'")
    case "$st" in *healthy*) ok=1; break ;; esac
    echo "  $p 等待 healthy（第 $i 次）：$st"
    sleep 10
  done
  if [ -z "$ok" ]; then
    echo "  [FAIL] $p 5 分钟内未 healthy，回滚请用 build/bin/*.bak-* 产物重新发布" >&2
    exit 1
  fi
  CONTAINER_MD5=$($SSH "$SERVER" "docker exec juhe-ai-go-$p md5sum /usr/local/bin/juhe-ai-go-project" | awk '{print $1}')
  if [ "$CONTAINER_MD5" != "${LOCAL_MD5[$p]}" ]; then
    echo "  [FAIL] $p 容器内二进制与本地新编译不一致: 本地 ${LOCAL_MD5[$p]} vs 容器 $CONTAINER_MD5" >&2
    exit 1
  fi
  echo "  $p healthy，容器内二进制 md5 闭环一致"
done

CODE=$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 15 "$HEALTH_URL" || true)
if [ "$CODE" != "200" ]; then
  echo "[FAIL] 公网健康检查 $HEALTH_URL 返回 $CODE（期望 200）" >&2
  exit 1
fi
echo "== 发布完成：${TARGETS[*]}，公网健康 $CODE =="
echo "提示：发布后按运维手册跑一次 maintenance --ensure-schema（幂等加法）。"
