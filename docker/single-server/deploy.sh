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
#   6. 迁移前置核验（2026-10-14 起，只读）：组装镜像前用 maintenance 只读
#      verify 命令核验两条新版 gateway/jobs 启动门禁——capability domain 阶段
#      记录已封存 m2_finalized、调度偏好迁移 stage=completed，任一未就绪立即
#      中止并指向维护窗口文档（此前先 build+up 后才提示迁移，顺序倒置会让
#      新版进程 fail-fast 循环）；maintenance 单独发布不核验（维护窗口的既定
#      顺序是先更 CLI 镜像、后跑迁移，此时门禁未就绪属预期）。
#   7. 部署配置同步（2026-10-14 起）：compose.yml / Caddyfile /
#      Dockerfile.runtime 与远端逐文件 md5 比对，漂移即自动同步上传（tar
#      管道，仅这三个文件，不触碰 .env），上传后复核；失败中止发布。远端
#      compose 的资源契约（gateway mem_limit/GOMEMLIMIT、jobs mem_limit）
#      不再依赖人工记忆补传。
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
# 主机层优化自检（BUG-0313，2026-10-14 起，幂等）：swap 兜底 / 防冻结 sysctl /
# journald 容量上限。每次发布自动执行一次，已收敛的机器秒级跳过；单项失败仅
# 告警不阻塞发布（与二进制发布正交，见 host-init.sh 头注）。新机初始化除本步
# 外的完整序列见 docs/deploy/部署指南.md 第 1、2 节。
# ---------------------------------------------------------------------------
echo "== 前置：主机层优化自检（幂等，BUG-0313）=="
if ! $SSH "$SERVER" 'bash -s' < "$SCRIPT_DIR/host-init.sh"; then
  echo "[WARN] 主机层优化存在未收敛项（上方输出），不影响本次发布；新机初始化请先单独修复重跑。" >&2
fi

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
  # 停写窗口形态（B4① 起窗口序列先 stop gateway/jobs 再发布）：容器停止/重建中
  # docker exec 不可用——远端命令尾缀 || true 防 pipefail 中止，LIVE_BUILD_ID 为空
  # 时回退读服务器 build/frontend-dist/build-info.json（上次发布上传、与容器内
  # dist 同源的产物）；两处都读不到才判定无法判定并中止。
  LIVE_BUILD_ID=$($SSH "$SERVER" "docker exec juhe-ai-go-gateway cat /app/frontend/dist/build-info.json 2>/dev/null || true" \
    | sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p')
  if [ -z "$LIVE_BUILD_ID" ]; then
    LIVE_BUILD_ID=$($SSH "$SERVER" "cat $SERVER_DIR/build/frontend-dist/build-info.json 2>/dev/null || true" \
      | sed -n 's/.*"buildId"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p')
  fi
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

# ---------------------------------------------------------------------------
# 部署配置同步（2026-10-14 起）：compose.yml / Caddyfile / Dockerfile.runtime
# 此前只在初装时上传，发布脚本不校验远端副本——远端 compose 的资源契约变更
# （gateway mem_limit=3g / GOMEMLIMIT=2300MiB、jobs mem_limit=1g 等）只能靠
# 人工记忆补传（部署指南旧表述"deploy.sh 只上传二进制"自证）。本步逐文件
# 比对本地与远端 md5（仿上方三点校验），不一致即自动同步上传：tar 管道只传
# 这三个文件（不触碰 .env），上传后复核，失败中止发布。
# ---------------------------------------------------------------------------
echo "== 部署配置同步检查（compose.yml / Caddyfile / Dockerfile.runtime）=="
CONFIG_FILES=(compose.yml Caddyfile Dockerfile.runtime)
CONFIG_DRIFTED=()
for f in "${CONFIG_FILES[@]}"; do
  LOCAL_CFG_MD5=$(md5sum "$SCRIPT_DIR/$f" | awk '{print $1}')
  REMOTE_CFG_MD5=$($SSH "$SERVER" "md5sum $SERVER_DIR/$f" 2>/dev/null | awk '{print $1}') || REMOTE_CFG_MD5=""
  if [ "$LOCAL_CFG_MD5" != "$REMOTE_CFG_MD5" ]; then
    CONFIG_DRIFTED+=("$f")
    echo "  $f 漂移：本地 $LOCAL_CFG_MD5 vs 远端 ${REMOTE_CFG_MD5:-<文件缺失>}"
  fi
done
if [ ${#CONFIG_DRIFTED[@]} -gt 0 ]; then
  echo "  漂移文件：${CONFIG_DRIFTED[*]}，自动同步上传（仅上述清单内文件，不触碰 .env）"
  tar czf - -C "$SCRIPT_DIR" "${CONFIG_FILES[@]}" | $SSH "$SERVER" "tar xzf - -C $SERVER_DIR"
  for f in "${CONFIG_FILES[@]}"; do
    LOCAL_CFG_MD5=$(md5sum "$SCRIPT_DIR/$f" | awk '{print $1}')
    REMOTE_CFG_MD5=$($SSH "$SERVER" "md5sum $SERVER_DIR/$f" 2>/dev/null | awk '{print $1}') || REMOTE_CFG_MD5=""
    if [ "$LOCAL_CFG_MD5" != "$REMOTE_CFG_MD5" ]; then
      echo "  [FAIL] $f 同步上传后 md5 仍不一致: 本地 $LOCAL_CFG_MD5 vs 远端 ${REMOTE_CFG_MD5:-<文件缺失>}" >&2
      echo "         远端目录可能不可写或上传通道异常，发布中止；手工检查 $SERVER_DIR 后重试。" >&2
      exit 1
    fi
  done
  echo "  配置同步完成并复核一致：${CONFIG_DRIFTED[*]}"
else
  echo "  compose.yml / Caddyfile / Dockerfile.runtime 远端副本与本地一致"
fi

# ---------------------------------------------------------------------------
# 迁移前置核验（2026-10-14 起，只读零写入）：新版 gateway/jobs 启动门禁要求
# capability domain 阶段记录已封存 m2_finalized（AI 账户能力域 M2）与调度偏好
# 迁移 stage=completed（P2），任一缺失都会 fail-fast 循环。此前流程先
# build+up、发布后才提示跑迁移，顺序倒置。这里在组装镜像前用 maintenance 的
# 只读 verify 命令核验（未就绪分别 exit 3/2，均按失败处理）。maintenance 的
# verify 路径无 env 回退，--dsn 必须显式传参（--driver 旗标已随 SQLite 退役删除）：非交互 SSH 命令先
# source 服务器 .env，DSN 以远端展开的 "$JUHE_AI_POSTGRES_URL" 传入（与
# README「拓扑」节一次性命令同源；deploy.sh 本地变量用 $SERVER_DIR 前缀区分）。
# maintenance 单独发布不核验：维护窗口的既定顺序是先更 CLI 镜像、后跑迁移
# （README「更新发布」节），此时门禁未就绪属预期，前置核验会让窗口流程死锁。
# ---------------------------------------------------------------------------
if [ "$TARGET" != "maintenance" ]; then
  echo "== 迁移前置核验（只读；未就绪先完成维护窗口再发布）=="
  if ! $SSH "$SERVER" "cd $SERVER_DIR && set -a && . ./.env && set +a && docker compose --profile tool run --rm --no-deps maintenance --verify-account-capability-domain --account-capability-domain-stage=m2-cutover --account-capability-domain-check=postflight --dsn \"\$JUHE_AI_POSTGRES_URL\""; then
    echo "[FAIL] 账户能力域迁移未就绪（要求 global_settings 阶段记录已封存 m2_finalized），新版 gateway/jobs 启动会 fail-fast 循环，发布中止。" >&2
    echo "       先按 docker/single-server/README.md「账户能力域迁移维护窗口」完成停写窗口迁移并以 postflight 核验通过，再重新发布。" >&2
    exit 1
  fi
  if ! $SSH "$SERVER" "cd $SERVER_DIR && set -a && . ./.env && set +a && docker compose --profile tool run --rm --no-deps maintenance --verify-scheduling-preferences --scheduling-preferences-check=postflight --dsn \"\$JUHE_AI_POSTGRES_URL\""; then
    echo "[FAIL] 调度偏好迁移未完成（scheduling_preferences_migration stage != completed），新版 gateway/jobs 启动会 fail-fast 循环，发布中止。" >&2
    echo "       先按 docker/single-server/README.md「调度偏好迁移维护窗口」完成停写窗口迁移并以 postflight 核验通过，再重新发布。" >&2
    exit 1
  fi
fi

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

if [ "$TARGET" = "maintenance" ]; then
  # 一次性 CLI 的镜像校验独立于常驻进程：维护窗口内 jobs 必须保持停止。
  # 覆盖 entrypoint 并禁止启动依赖，只读取新镜像二进制，不执行迁移。
  echo "== [5/6] maintenance 镜像内二进制闭环校验 =="
  CONTAINER_MD5=$($SSH "$SERVER" "cd $SERVER_DIR && docker compose --profile tool run --rm --no-deps --entrypoint md5sum maintenance /usr/local/bin/juhe-ai-go-project" | awk '{print $1}')
  if [ "$CONTAINER_MD5" != "${LOCAL_MD5[maintenance]}" ]; then
    echo "[FAIL] maintenance 镜像内二进制与本地新编译不一致: 本地 ${LOCAL_MD5[maintenance]} vs 镜像 $CONTAINER_MD5" >&2
    exit 1
  fi
  echo "== [6/6] maintenance 镜像更新并校验完成；回填完成、恢复 gateway/jobs 后执行运行验证 =="
  exit 0
fi

echo "== [5/6] 健康 check + 容器运行二进制闭环校验 =="
for p in "${TARGETS[@]}"; do
  [ "$p" = "maintenance" ] && continue  # maintenance 是一次性 tool 容器，无常驻进程
  ok=""
  for i in $(seq 1 30); do
    # BUG-0301：docker compose ps 的 Status 文案里 "unhealthy" 含子串 "healthy"，
    # `*healthy*` 子串判断会把 unhealthy 误判为已通过；改读 docker inspect 的
    # 结构化健康字段（gateway/jobs 容器名固定为 juhe-ai-go-$p）并做精确相等比较。
    # `|| st=""` 是 set -e 兜底：单轮查询失败（容器不存在、ssh 失败等）不得中止
    # 整个发布，按未 healthy 继续等待至预算耗尽 fail-closed。
    st=$($SSH "$SERVER" "docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' juhe-ai-go-$p" 2>/dev/null) || st=""
    if [ "$st" = "healthy" ]; then ok=1; break; fi
    echo "  $p 等待 healthy（第 $i 次）：${st:-<状态查询失败>}"
    sleep 10
  done
  if [ -z "$ok" ]; then
    echo "  [FAIL] $p 5 分钟内未 healthy，回滚请用 build/bin/*.bak-* 产物重新发布" >&2
    echo "         若为启动门禁失败（capability domain 未封存 m2_finalized / 调度偏好迁移未完成），先按 docker/single-server/README.md「更新发布」节维护窗口完成迁移再重试。" >&2
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

CODE=$(curl -fsS --ssl-no-revoke -o /dev/null -w '%{http_code}' --max-time 15 "$HEALTH_URL" || true)
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
echo "提示：发布后按运维手册跑一次 maintenance --ensure-schema（已恢复幂等加法；studio 退场清理已移出普通 ensure，走显式 --apply-studio-retirement；含删列等破坏性收敛的 ensure 步骤必须在写者停止后执行——先看 docker/single-server/README.md「更新发布」节维护窗口，勿在 gateway/jobs 运行时直接跑）。"
