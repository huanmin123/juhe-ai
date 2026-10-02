#!/usr/bin/env bash
# 国内单机生产发布脚本（目标服务器见 .local 资产与 .env，go-only 单机 Docker）。
#
# 防呆设计（对应 2026-09-26 部署事故：shared 模块修复后 gateway 用旧产物上线）：
#   1. 每次发布无条件全量重编译目标二进制（不信任 build/bin 里的既有产物）；
#   2. md5 三点闭环：本地新编译 = 服务器 build/bin = 容器内运行二进制；
#   3. 逐容器等待 healthy 后才判定成功（BUG-0228 起 compose 为 gateway 配
#      JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=45s：发布 recreate 窗口新 gateway 在启动期
#      等待前任 F3/F4 租约 TTL（默认 30s）过期后接管，等待计入 healthcheck
#      start_period=75s，healthy 滞后至多约 1 分钟属预期，不再有重启循环）；
#   4. 发布后验证（verify-release.sh，PLAN-20261002T114140718Z）：usage spool
#      同源与 jobs drain 接线为强制断言；服务器配置一次性凭据文件
#      .release-verify/api-key 后，追加"一条 /v1 请求 → 审计 + 用量落库"
#      闭环门禁（缺凭据文件时该项告警跳过，不阻塞发布）。
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
# 目标服务器属实例事实，不入库（BUG-0225）：优先环境变量 JUHE_AI_DEPLOY_SERVER，
# 其次同目录 .env（gitignore）里的同名变量；两者皆缺时 fail-fast。
if [ -z "${JUHE_AI_DEPLOY_SERVER:-}" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  JUHE_AI_DEPLOY_SERVER=$(grep '^JUHE_AI_DEPLOY_SERVER=' "$SCRIPT_DIR/.env" | tail -n 1 | cut -d= -f2- | tr -d '\r"'\' || true)
fi
if [ -z "${JUHE_AI_DEPLOY_SERVER:-}" ]; then
  echo "[FAIL] 缺少 JUHE_AI_DEPLOY_SERVER（ssh 目标，<user>@<IP>）。" >&2
  echo "       从 .local/project-resources/prod/assets/ 服务器资产台账获取并写入 docker/single-server/.env：" >&2
  echo "         echo 'JUHE_AI_DEPLOY_SERVER=<user>@<服务器IP>' >> docker/single-server/.env" >&2
  exit 1
fi
SERVER="$JUHE_AI_DEPLOY_SERVER"
SERVER_DIR=/opt/juhe-ai
SSH="ssh -i $REPO_ROOT/.local/project-resources/prod/assets/ssh/juhe_ai_cn103_ed25519 -o BatchMode=yes -o ConnectTimeout=15"
HEALTH_URL=https://aijh.huanmin.top/__aisys__/health

TARGET="${1:-all}"
case "$TARGET" in
  # all 含 maintenance：一次性 CLI 的 schema/seed 变更必须随发版落地，否则
  # 发布后 ensure-schema 用旧清单"幂等成功"却漏建新表（2026-10-02/03 两次
  # 同款事故：623/624 语句一致掩盖 chat/j3b 迁移未落地）。maintenance 分支
  # 只编译+上传+重建镜像，不 up（一次性容器 up -d 会等入口退出而挂起）。
  all) TARGETS=(gateway jobs maintenance) ;;
  gateway|jobs|maintenance) TARGETS=("$TARGET") ;;
  *) echo "用法: $0 [all|gateway|jobs|maintenance]" >&2; exit 1 ;;
esac

cd "$REPO_ROOT"
echo "== [1/6] 全量重编译（linux/amd64，不信任既有产物）=="
BUILD_BIN=docker/single-server/build/bin
mkdir -p "$BUILD_BIN"
declare -A LOCAL_MD5
for p in "${TARGETS[@]}"; do
  (cd "backend-go/projects/$p" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "../../../$BUILD_BIN/juhe-ai-$p" "./cmd/juhe-ai-$p")
  LOCAL_MD5[$p]=$(md5sum "$BUILD_BIN/juhe-ai-$p" | awk '{print $1}')
  echo "  juhe-ai-$p  ${LOCAL_MD5[$p]}"
done

echo "== [2/6] 上传到 $SERVER:$SERVER_DIR/build/bin =="
tar czf - -C "$BUILD_BIN" ${TARGETS[@]/#/juhe-ai-} \
  | $SSH "$SERVER" "tar xzf - -C $SERVER_DIR/build/bin && chmod 0755 $SERVER_DIR/build/bin/juhe-ai-*"

echo "== [3/6] 服务器侧 md5 比对（上传完整性）=="
for p in "${TARGETS[@]}"; do
  SERVER_MD5=$($SSH "$SERVER" "md5sum $SERVER_DIR/build/bin/juhe-ai-$p" | awk '{print $1}')
  if [ "$SERVER_MD5" != "${LOCAL_MD5[$p]}" ]; then
    echo "  [FAIL] juhe-ai-$p 上传后 md5 不一致: 本地 ${LOCAL_MD5[$p]} vs 服务器 $SERVER_MD5" >&2
    exit 1
  fi
  echo "  juhe-ai-$p  OK"
done

echo "== [4/6] 服务器组装镜像并滚动更新 =="
# maintenance 只重建镜像不 up：一次性 CLI 容器 up -d 会等待入口进程退出而
# 挂起（2026-10-03 实测）；常驻进程照常滚动。
UP_TARGETS=()
for p in "${TARGETS[@]}"; do
  [ "$p" = "maintenance" ] || UP_TARGETS+=("$p")
done
if [ ${#UP_TARGETS[@]} -gt 0 ]; then
  $SSH "$SERVER" "cd $SERVER_DIR && docker compose build ${TARGETS[*]} 2>&1 | tail -1 && docker compose up -d ${UP_TARGETS[*]} 2>&1 | tail -2"
else
  $SSH "$SERVER" "cd $SERVER_DIR && docker compose build ${TARGETS[*]} 2>&1 | tail -1"
fi

echo "== [5/6] 健康 check + 容器运行二进制闭环校验 =="
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
echo "== [6/6] 发布后验证（spool 同源 + drain 接线 + 业务闭环）=="
$SSH "$SERVER" "cat > $SERVER_DIR/verify-release.sh && chmod 0755 $SERVER_DIR/verify-release.sh" < "$SCRIPT_DIR/verify-release.sh"
if ! $SSH "$SERVER" "cd $SERVER_DIR && bash verify-release.sh"; then
  echo "[FAIL] 发布后验证未通过（脚本输出见上；闭环分诊要点：spool 文件数 / 近 5 分钟落库行数 / 容器状态）。" >&2
  echo "       发布产物本身已完成 build+up，按验证输出定位问题后重跑：ssh 到服务器 cd $SERVER_DIR && bash verify-release.sh" >&2
  exit 1
fi

echo "== 发布完成：${TARGETS[*]}，公网健康 $CODE，发布后验证通过 =="
echo "提示：发布后按运维手册跑一次 maintenance --ensure-schema（幂等加法）。"
