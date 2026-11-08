#!/usr/bin/env bash
# T184 干跑回归：验证 deploy.sh upgrade 模式两处修复的远端命令串——
#   1. UPGRADE_CMD 带 cd $SERVER_DIR（2026-11-08 生产实录：缺 cd 致 ./.env 不存在）；
#   2. BACKUP_VERIFY_CMD 非交互备份旁路的引号链与三态行为（无 dump 失败 /
#      新 dump 通过 / pg_restore 失败中止）。
# 不连真实服务器：CMD 定义用 sed 从 deploy.sh 同源抽取（防复制漂移），
# ssh/docker 为 stub，直接以本地 bash -c 模拟远端 `bash -c '...'` 单引号包裹
# 形态执行，专门覆盖「内嵌双引号/$()/无单引号」的展开链。
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
DEPLOY_SH="$SCRIPT_DIR/deploy.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# ---------- 同源抽取 CMD 定义（去前导缩进，SERVER_DIR/REMOTE_COMPOSE_ENV 本地预置） ----------
SERVER_DIR="$WORK/opt/juhe-ai"
REMOTE_COMPOSE_ENV=""
eval "$(sed -n 's/^ *BACKUP_VERIFY_CMD="/BACKUP_VERIFY_CMD="/p' "$DEPLOY_SH")"
eval "$(sed -n 's/^ *UPGRADE_CMD="/UPGRADE_CMD="/p' "$DEPLOY_SH")"
[ -n "${BACKUP_VERIFY_CMD:-}" ] || { echo "[FAIL] 未从 deploy.sh 抽到 BACKUP_VERIFY_CMD"; exit 1; }
[ -n "${UPGRADE_CMD:-}" ] || { echo "[FAIL] 未从 deploy.sh 抽到 UPGRADE_CMD"; exit 1; }

# ---------- stub 环境 ----------
mkdir -p "$WORK/bin" "$SERVER_DIR/backup" "$SERVER_DIR/opt"
printf 'JUHE_AI_POSTGRES_URL=postgres://fake\nJUHE_AI_SECRET=fake-secret\n' > "$SERVER_DIR/.env"
cat > "$WORK/bin/docker" << 'STUB'
#!/usr/bin/env bash
# stub 参数：$1=子命令（cp/exec），$2 起为参数（bash 不把脚本名计入 $1）
LOG=${DEPLOY_MOCK_LOG:?}
echo "docker $*" >> "$LOG"
case "$1" in
  cp)
    src="$2"; dst="$3"
    # 容器 cp 语义：dst 形如 container:/path，stub 剥掉前缀落到本地路径
    dst="${dst#*:}"
    cp "$src" "$dst" && exit 0
    exit 1 ;;
  exec)
    # 形态：exec juhe-ai-postgres pg_restore|rm ...
    sub="$3"
    f=""
    for a in "$@"; do case "$a" in /tmp/*) f="$a";; esac; done
    case "$sub" in
      pg_restore)
        [ -s "$f" ] || exit 4
        head -c 5 "$f" | grep -q "PGDMP" || exit 5
        exit 0 ;;
      rm)
        rm -f "$f"; exit 0 ;;
    esac
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$WORK/bin/docker"
export PATH="$WORK/bin:$PATH"
export DEPLOY_MOCK_LOG="$WORK/docker.log"

# 远端执行形态：与 deploy.sh 相同的 `bash -c '<CMD>'` 单引号包裹。
remote() { bash -c "$1"; }

fail() { echo "[FAIL] $1" >&2; exit 1; }
pass() { echo "[PASS] $1"; }

# ---------- 用例 1：UPGRADE_CMD 的 cd 生效（.env 可读= cwd 正确） ----------
# .env 在 $SERVER_DIR 下；若 cd 缺失则 `. ./.env` 失败（cwd=测试工作目录，无 .env）。
if (cd "$WORK" && remote "$UPGRADE_CMD" >/dev/null 2>&1); then
  # 远端语义里 . ./.env 的变量是否真的进入命令链：grep 日志确认 compose stub 被调用
  grep -q "compose --profile tool run --rm maintenance upgrade" "$DEPLOY_MOCK_LOG" 2>/dev/null \
    || fail "UPGRADE_CMD 未到达 compose 调用"
  pass "UPGRADE_CMD：cd $SERVER_DIR 生效，./.env 读取成功并到达 upgrade 调用"
else
  fail "UPGRADE_CMD 执行失败（cd 缺失或引号链断裂）"
fi

# ---------- 用例 2：备份旁路无 dump → 校验失败 ----------
rm -f "$SERVER_DIR"/backup/*.dump 2>/dev/null || true
if remote "$BACKUP_VERIFY_CMD" >/dev/null 2>&1; then
  fail "无 dump 时旁路校验不应通过"
fi
pass "备份旁路：无 juhe_ai-pre-upgrade-*.dump 时校验失败中止"

# ---------- 用例 3：24h 内有效 dump → 校验通过（cp + pg_restore stub + 清理） ----------
printf 'PGDMP' > "$SERVER_DIR/backup/juhe_ai-pre-upgrade-20261108-0000.dump"
:; truncate -s 64 "$SERVER_DIR/backup/juhe_ai-pre-upgrade-20261108-0000.dump" 2>/dev/null || true
printf 'PGDMP fake toc bytes for mock verify 0123456789abcdef' > "$SERVER_DIR/backup/juhe_ai-pre-upgrade-20261108-0000.dump"
: > "$DEPLOY_MOCK_LOG"
if remote "$BACKUP_VERIFY_CMD" >/dev/null 2>&1; then
  grep -q "pg_restore -U juhe_ai -l /tmp/deploy-backup-verify.dump" "$DEPLOY_MOCK_LOG" \
    || fail "旁路未执行容器内 pg_restore -l 校验"
  grep -q "rm -f /tmp/deploy-backup-verify.dump" "$DEPLOY_MOCK_LOG" \
    || fail "旁路未清理容器内临时校验文件"
  pass "备份旁路：有效 dump 走 cp → pg_restore -l → 清理全链"
else
  fail "有效 dump 时旁路校验应通过"
fi

# ---------- 用例 4：pg_restore 校验失败（非 PGDMP 魔数）→ 中止 ----------
printf 'NOTPG' > "$SERVER_DIR/backup/juhe_ai-pre-upgrade-20261108-0000.dump"
if remote "$BACKUP_VERIFY_CMD" >/dev/null 2>&1; then
  fail "pg_restore 校验失败时旁路不应通过"
fi
pass "备份旁路：dump 不可读（魔数不符）时校验失败中止"

echo "== T184 干跑回归全部通过 =="
