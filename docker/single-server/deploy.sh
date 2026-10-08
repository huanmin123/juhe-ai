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
#      同源与 jobs drain 接线为强制断言。（原可选"/v1 请求 → 审计 + 用量落库"
#      闭环门禁已于 2026-10-03 按用户裁定移除：依赖账户/模型实时可用性无法
#      保证稳定；见 README 发布后验证节。）
#   5. 前端漏发门禁（2026-10-08 起，对应 10-07/10-08 多次前端漏发：deploy.sh 只
#      传 Go 二进制，前端 dist 经 Dockerfile.runtime COPY 烤入镜像，本地 dist 不
#      重建时后端发版会把旧前端再次烤进新镜像）：发布前探测线上前端 buildId，
#      前端相对该版本有任何改动（已提交或工作区未提交）即自动 pnpm build + 上传
#      dist，且校验产物 buildId 等于 HEAD、产物内容新于全部前端改动，否则中止。
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

# ---------------------------------------------------------------------------
# 前端漏发门禁（2026-10-08 起）。
#
# 判据：线上正在提供服务的前端版本 = gateway 容器内
# /app/frontend/dist/build-info.json 的 buildId（容器内 dist 才是真实服务的
# 产物；宿主 build/frontend-dist 只是下次 compose build 的输入，二者可能不一致）。
# 前端范围（frontend/）相对该 commit 有任何差异——已提交的提交或工作区未提交
# 改动——即视为"前端有改动"，本次发布必须携带新前端，否则发布中止。
#
# 满足判据时脚本自动执行：pnpm build（产物 buildId 必须等于当前 HEAD）→
# 服务器旧目录备份 frontend-dist.bak-<时间戳> → 上传 → 产物内容必须新于全部
# 前端源文件（防止 build 期间又有新改动写入）。前端 dist 由 Dockerfile.runtime
# COPY 进镜像，本步骤只负责让服务器 build/frontend-dist 成为新产物，真正生效
# 仍依赖后续 gateway 镜像重建；因此本门禁只在本次发布会重建 gateway 镜像时
# 执行（jobs / maintenance 单独发布不触碰前端）。
# ---------------------------------------------------------------------------
FRONTEND_REBUILD=0
if [[ " ${TARGETS[*]} " == *" gateway "* ]]; then
  echo "== [0/6] 前端漏发门禁 =="
  LIVE_BUILD_ID=$($SSH "$SERVER" "docker exec juhe-ai-go-gateway cat /app/frontend/dist/build-info.json" \
    | sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p')
  if [ -z "$LIVE_BUILD_ID" ]; then
    echo "[FAIL] 无法读取线上前端 buildId（gateway 容器内 /app/frontend/dist/build-info.json）。" >&2
    echo "       前端版本未知时不能判定是否漏发，发布中止；确认容器状态后重试。" >&2
    exit 1
  fi
  if ! git cat-file -e "$LIVE_BUILD_ID^{commit}" 2>/dev/null; then
    echo "[FAIL] 线上前端 buildId $LIVE_BUILD_ID 在本地仓库不存在（未 fetch 或产物来自其他仓库），无法做差异判定，发布中止。" >&2
    exit 1
  fi
  # 三段并集：线上版本以来已提交的差异、已跟踪文件的未提交差异、未跟踪新文件
  # （git diff 不列未跟踪文件——漏掉它就会把"新增前端文件"误判为无改动）。
  FRONTEND_DIFF=$(git diff --name-only "$LIVE_BUILD_ID" -- frontend/ \
    && git diff --name-only HEAD -- frontend/ \
    && git ls-files --others --exclude-standard -- frontend/)
  FRONTEND_DIFF=$(printf '%s\n' "$FRONTEND_DIFF" | sed '/^$/d' | sort -u)
  if [ -n "$FRONTEND_DIFF" ]; then
    echo "  线上前端 $LIVE_BUILD_ID 落后于本地前端改动，本次发布将重建并上传前端："
    printf '    %s\n' $FRONTEND_DIFF
    if [ ! -x frontend/node_modules/.bin/vite ]; then
      echo "[FAIL] frontend/node_modules 缺失，先在 frontend/ 执行 pnpm install 再发布。" >&2
      exit 1
    fi
    # 产物内容校验的时间基线取构建开始前：构建后落盘的源文件改动不会被误判，
    # 构建期间发生的源文件改动会被检出并中止。
    FRONTEND_BASELINE=$(mktemp)
    (cd frontend && pnpm build)
    echo "== [0/6] 前端产物校验 =="
    DIST_BUILD_ID=$(sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p' frontend/dist/build-info.json)
    HEAD_COMMIT=$(git rev-parse HEAD)
    if [ "$DIST_BUILD_ID" != "$HEAD_COMMIT" ]; then
      echo "[FAIL] 前端产物 buildId=$DIST_BUILD_ID 与当前 HEAD=$HEAD_COMMIT 不一致（构建期间 HEAD 被移动，或设置了 VITE_JUHE_AI_BUILD_ID 覆盖）。" >&2
      echo "       产物不能代表当前代码，发布中止；确认后重跑。" >&2
      exit 1
    fi
    # mtime 在 Windows 工作区与容器化构建间会有秒级抖动，给 2 秒容忍。
    # tsconfig.tsbuildinfo 是 vue-tsc 增量构建缓存（gitignored 构建产物），
    # 每次构建必被改写，不属于源文件改动（2026-10-08 首次触发误报修复）。
    if find frontend -path frontend/dist -prune -o -path frontend/node_modules -prune -o -path frontend/tsconfig.tsbuildinfo -prune -o -type f -newer "$FRONTEND_BASELINE" -print | grep -q .; then
      echo "[FAIL] 前端源文件在构建期间被修改，产物可能未包含最新改动：" >&2
      find frontend -path frontend/dist -prune -o -path frontend/node_modules -prune -o -path frontend/tsconfig.tsbuildinfo -prune -o -type f -newer "$FRONTEND_BASELINE" -print >&2
      echo "       发布中止；待改动稳定后重跑（脚本会重新构建）。" >&2
      rm -f "$FRONTEND_BASELINE"
      exit 1
    fi
    rm -f "$FRONTEND_BASELINE"
    rm -rf docker/single-server/build/frontend-dist
    cp -r frontend/dist docker/single-server/build/frontend-dist
    echo "== [0/6] 上传前端到 $SERVER:$SERVER_DIR/build/frontend-dist（旧目录备份）=="
    tar czf - -C docker/single-server/build frontend-dist \
      | $SSH "$SERVER" "set -e; cd $SERVER_DIR/build && mv frontend-dist frontend-dist.bak-\$(date +%m%d-%H%M%S) && tar xzf -"
    SERVER_DIST_ID=$($SSH "$SERVER" "sed -n 's/.*\"buildId\"[[:space:]]*:[[:space:]]*\"\([0-9a-f]\{40\}\)\".*/\1/p' $SERVER_DIR/build/frontend-dist/build-info.json")
    if [ "$SERVER_DIST_ID" != "$HEAD_COMMIT" ]; then
      echo "[FAIL] 服务器前端产物 buildId=$SERVER_DIST_ID 与本地 HEAD=$HEAD_COMMIT 不一致，上传可能不完整，发布中止。" >&2
      exit 1
    fi
    FRONTEND_REBUILD=1
    echo "  前端产物已上传并通过校验（buildId=$SERVER_DIST_ID），将随本次 gateway 镜像重建生效"
  else
    echo "  前端相对线上版本 $LIVE_BUILD_ID 无改动，沿用现有前端产物"
  fi
fi

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
# 远端构建/启动失败必须传播（本地 set -euo pipefail 管不到远端 shell）：远端
# login shell 默认无 pipefail 时，`build | tail` 管道退出码由 tail 决定，build
# 失败仍返回 0 且 && 会继续用旧镜像 up 滚动重启生产容器（假成功）。显式 bash -c
# + pipefail 让真实退出码经 ssh 返回本地，tail 摘要输出体验不变。
REMOTE_CMD="set -o pipefail; cd $SERVER_DIR && docker compose build ${TARGETS[*]} 2>&1 | tail -1"
if [ ${#UP_TARGETS[@]} -gt 0 ]; then
  REMOTE_CMD="$REMOTE_CMD && docker compose up -d ${UP_TARGETS[*]} 2>&1 | tail -2"
fi
if ! $SSH "$SERVER" "bash -c '$REMOTE_CMD'"; then
  echo "[FAIL] 服务器侧 docker compose build/up 失败（上方为 tail 摘要），发布已中止，未继续后续步骤。" >&2
  echo "       完整错误：ssh 到服务器 cd $SERVER_DIR 后手动执行对应 compose 命令查看。" >&2
  exit 1
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
  # 前端闭环：本次发布重建了前端时，容器内实际提供服务的前端必须就是刚上传的产物
  # （宿主目录更新但镜像未重烤 = 线上依旧是旧前端，正是本门禁要消灭的漏发形态）。
  if [ "$p" = "gateway" ] && [ "$FRONTEND_REBUILD" = "1" ]; then
    CONTAINER_DIST_ID=$($SSH "$SERVER" "docker exec juhe-ai-go-gateway cat /app/frontend/dist/build-info.json" \
      | sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p')
    if [ "$CONTAINER_DIST_ID" != "$(git rev-parse HEAD)" ]; then
      echo "  [FAIL] gateway 容器内前端 buildId=$CONTAINER_DIST_ID 与本次构建的 HEAD 不一致，前端未随镜像生效。" >&2
      echo "         回滚：服务器 build/frontend-dist.bak-* 恢复后重新 docker compose build gateway && up -d gateway。" >&2
      exit 1
    fi
    echo "  gateway 容器内前端 buildId 闭环一致（$CONTAINER_DIST_ID）"
  fi
done

CODE=$(curl -fsS -o /dev/null -w '%{http_code}' --max-time 15 "$HEALTH_URL" || true)
if [ "$CODE" != "200" ]; then
  echo "[FAIL] 公网健康检查 $HEALTH_URL 返回 $CODE（期望 200）" >&2
  exit 1
fi
echo "== [6/6] 发布后验证（spool 同源 + drain 接线）=="
$SSH "$SERVER" "cat > $SERVER_DIR/verify-release.sh && chmod 0755 $SERVER_DIR/verify-release.sh" < "$SCRIPT_DIR/verify-release.sh"
if ! $SSH "$SERVER" "cd $SERVER_DIR && bash verify-release.sh"; then
  echo "[FAIL] 发布后验证未通过（脚本输出见上；分诊要点：spool 同源挂载 / jobs drain 告警 / 容器状态）。" >&2
  echo "       发布产物本身已完成 build+up，按验证输出定位问题后重跑：ssh 到服务器 cd $SERVER_DIR && bash verify-release.sh" >&2
  exit 1
fi

echo "== 发布完成：${TARGETS[*]}，公网健康 $CODE，发布后验证通过 =="
echo "提示：发布后按运维手册跑一次 maintenance --ensure-schema（幂等加法）。"
