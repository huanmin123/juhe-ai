#!/usr/bin/env bash
# 发布后验证脚本：在目标服务器 compose 目录（如 /opt/juhe-ai）下运行；
# deploy.sh 第 [6/6] 步自动上传并调用，也可手动运行。
#
# 检查项：
#   1. usage-record-spool 同源断言（无凭据，强制）：gateway 与 jobs 的
#      /app/backend/data 挂载必须解析到同一宿主机目录，且两侧容器内
#      usage-record-spool 目录存在——不同源时接口照常 200 但用量永不到库
#      （BUG-0193），只能在事故后人工 triage；本检查把该不变量变成发布门禁。
#   2. jobs drain 接线断言（无凭据，强制）：jobs 近 10 分钟日志不得出现
#      usage_record_spool_drain_unwired 告警（未接线时统计同样恒空）。
#   3. 业务闭环（可选，需一次性配置凭据）：从 gateway 容器内发一条最小
#      /v1/chat/completions 请求，按响应头 X-Trace-Id 断言
#      juhe_dataset.audit_logs 与 juhe_usage.usage_records 各至少落一行
#      （请求 → 审计 → spool → jobs drain → PG 全链路）。
#
# 一次性凭据配置（可选但强烈建议；缺省时第 3 项跳过并告警，不阻塞发布）：
#   mkdir -p .release-verify && chmod 700 .release-verify
#   echo -n '<网关API Key>' > .release-verify/api-key && chmod 600 .release-verify/api-key
#   echo -n '<模型ID>' > .release-verify/model   # 可选；缺省取 /v1/models 第一项
# 建议使用绑定稳定测试账户的专用 API Key；Key 经 docker exec 环境变量传入
# 容器内 wget，不出现在本脚本、日志或进程列表参数中。
#
# 用法：cd <compose 目录> && bash verify-release.sh
#   JUHE_AI_RELEASE_VERIFY_TIMEOUT  闭环落库轮询超时秒数，默认 180
# 退出码：0 = 全部通过（或闭环未配置仅告警）；1 = 任一强制项失败或闭环失败。
set -euo pipefail

cd "$(dirname "$0")"
COMPOSE_DIR=$(pwd)
GW_CONTAINER=juhe-ai-go-gateway
JOBS_CONTAINER=juhe-ai-go-jobs
TIMEOUT="${JUHE_AI_RELEASE_VERIFY_TIMEOUT:-180}"
FAIL=0

fail() { echo "  [FAIL] $*" >&2; FAIL=1; }
pass() { echo "  [PASS] $*"; }

psql_count() { # psql_count <SQL> —— 输出单个整数字段
  docker compose exec -T postgres psql -U juhe_ai -d juhe_ai -tAc "$1" | tr -d '[:space:]'
}

echo "== 发布后验证（compose 目录：$COMPOSE_DIR）=="

# ---------------------------------------------------------------------------
# 1. usage-record-spool 同源断言（BUG-0193 防复发）
# ---------------------------------------------------------------------------
echo "-- [1/3] usage-record-spool 同源断言"
GW_SRC=$(docker inspect "$GW_CONTAINER" --format '{{range .Mounts}}{{if eq .Destination "/app/backend/data"}}{{.Source}}{{end}}{{end}}')
JOBS_SRC=$(docker inspect "$JOBS_CONTAINER" --format '{{range .Mounts}}{{if eq .Destination "/app/backend/data"}}{{.Source}}{{end}}{{end}}')
echo "  gateway 挂载源：${GW_SRC:-<未挂载 /app/backend/data>}"
echo "  jobs    挂载源：${JOBS_SRC:-<未挂载 /app/backend/data>}"
if [ -z "$GW_SRC" ] || [ -z "$JOBS_SRC" ]; then
  fail "gateway/jobs 任一容器未挂载 /app/backend/data（working_dir 锚定被破坏）"
elif [ "$GW_SRC" != "$JOBS_SRC" ]; then
  fail "gateway 与 jobs 的 data 挂载不同源（$GW_SRC != $JOBS_SRC）：接口可能正常但用量记录永不到库"
else
  pass "两侧挂载同源：$GW_SRC"
fi
for c in "$GW_CONTAINER" "$JOBS_CONTAINER"; do
  if ! docker exec "$c" ls -ld /app/backend/data/usage-record-spool >/dev/null 2>&1; then
    fail "$c 容器内 /app/backend/data/usage-record-spool 不存在（首次运行会自动创建；gateway 写侧启动诊断会打印解析路径）"
  fi
done
[ "$FAIL" -eq 0 ] && pass "spool 目录两侧均存在"

# ---------------------------------------------------------------------------
# 2. jobs drain 接线断言
# ---------------------------------------------------------------------------
echo "-- [2/3] jobs usage spool drain 接线断言"
UNWIRED=$(docker compose logs jobs --since 10m 2>&1 | grep -c 'usage_record_spool_drain_unwired' || true)
if [ "${UNWIRED:-0}" -gt 0 ]; then
  fail "jobs 近 10 分钟出现 usage_record_spool_drain_unwired（近 $UNWIRED 次）：用量交接未接线，统计将恒空"
else
  pass "jobs 近 10 分钟无 drain 未接线告警"
fi

# ---------------------------------------------------------------------------
# 3. 业务闭环（可选）：一条 /v1 请求 → audit_logs + usage_records 落库
# ---------------------------------------------------------------------------
echo "-- [3/3] 业务闭环（一条 /v1/chat/completions → 审计 + 用量落库）"
KEY_FILE=.release-verify/api-key
MODEL_FILE=.release-verify/model
if [ ! -f "$KEY_FILE" ]; then
  echo "  [WARN] 未配置 $KEY_FILE，闭环检查跳过（不阻塞本次发布）。" >&2
  echo "         一次性配置后本项变为强制门禁：见本脚本头部注释。" >&2
  if [ "$FAIL" -eq 0 ]; then echo "== 结果：PASS（闭环未验证）=="; else echo "== 结果：FAIL ==" >&2; fi
  exit "$FAIL"
fi
KEY=$(cat "$KEY_FILE")
[ -n "$KEY" ] || { fail "api-key 文件为空"; exit 1; }

if [ -f "$MODEL_FILE" ] && [ -s "$MODEL_FILE" ]; then
  MODEL=$(cat "$MODEL_FILE")
else
  MODELS=$(docker compose exec -T -e VERIFY_KEY="$KEY" gateway sh -c \
    'wget -q -T 30 -O - --header="Authorization: Bearer $VERIFY_KEY" http://127.0.0.1:3306/v1/models' \
    || { fail "/v1/models 获取失败（Key 无效/网关不可用；响应头应返回 401 或 200）"; exit 1; })
  MODEL=$(printf '%s' "$MODELS" | grep -o '"id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/^"id"[[:space:]]*:[[:space:]]*"\([^"]*\)"$/\1/')
fi
[ -n "$MODEL" ] || { fail "未取得请求模型（model 文件为空且 /v1/models 无条目）"; exit 1; }
echo "  请求模型：$MODEL"

REQ_BODY=$(printf '{"model":"%s","messages":[{"role":"user","content":"release-verify"}],"max_tokens":1,"stream":false}' "$MODEL")
OUT=$(mktemp)
if ! docker compose exec -T -e VERIFY_KEY="$KEY" -e VERIFY_BODY="$REQ_BODY" gateway sh -c '
  wget -S -T 300 -O /tmp/release-verify-body.json \
    --header="Authorization: Bearer $VERIFY_KEY" \
    --header="Content-Type: application/json" \
    --post-data="$VERIFY_BODY" \
    http://127.0.0.1:3306/v1/chat/completions 2>/tmp/release-verify-hdrs
  rc=$?
  echo "---BODY---"; cat /tmp/release-verify-body.json 2>/dev/null
  echo; echo "---HDRS---"; cat /tmp/release-verify-hdrs 2>/dev/null
  rm -f /tmp/release-verify-body.json /tmp/release-verify-hdrs
  exit $rc
' >"$OUT" 2>&1; then
  echo "  [FAIL] /v1/chat/completions 请求本身失败（网关 5xx/超时/上游不可达，区别于落库缺失）。响应摘要：" >&2
  tail -c 400 "$OUT" >&2; echo >&2
  rm -f "$OUT"
  exit 1
fi
STATUS=$(sed -n '/---HDRS---/,$p' "$OUT" | grep -i -m1 'HTTP/' | awk '{print $2}' | tr -d '\r')
TRACE=$(sed -n '/---HDRS---/,$p' "$OUT" | grep -i -m1 'trace-id' | awk '{print $2}' | tr -d '\r')
echo "  HTTP 状态：${STATUS:-<未解析>}；X-Trace-Id：${TRACE:-<未解析>}"
case "$TRACE" in
  ''|*[!A-Za-z0-9-]*) fail "未能从响应头解析出合法 X-Trace-Id（busybox wget -S 行为异常时先人工核对响应头）"; exit 1 ;;
esac

AUDIT_N=0; USAGE_N=0
deadline=$((SECONDS + TIMEOUT))
while [ "$SECONDS" -lt "$deadline" ]; do
  AUDIT_N=$(psql_count "SELECT count(*) FROM juhe_dataset.audit_logs WHERE trace_id='$TRACE'")
  USAGE_N=$(psql_count "SELECT count(*) FROM juhe_usage.usage_records WHERE trace_id='$TRACE'")
  if [ "${AUDIT_N:-0}" -ge 1 ] && [ "${USAGE_N:-0}" -ge 1 ]; then break; fi
  sleep 5
done
echo "  落库轮询结果：audit_logs=$AUDIT_N 行，usage_records=$USAGE_N 行（trace=$TRACE，等待上限 ${TIMEOUT}s）"
if [ "${AUDIT_N:-0}" -ge 1 ] && [ "${USAGE_N:-0}" -ge 1 ]; then
  pass "业务闭环完整：请求成功且审计与用量均已落库"
else
  fail "闭环未完成（audit=$AUDIT_N / usage=$USAGE_N）。分诊："
  echo "    - gateway 视角 spool 文件数：$(docker exec "$GW_CONTAINER" find /app/backend/data/usage-record-spool -type f 2>/dev/null | wc -l)（>0 且 usage=0 → jobs drain 未消费）" >&2
  echo "    - 近 5 分钟 usage_records 总行数：$(psql_count "SELECT count(*) FROM juhe_usage.usage_records WHERE created_at > now() - interval '5 minutes'")（其他流量正常而本 trace=0 → 本请求路径问题）" >&2
  echo "    - 近 5 分钟 audit_logs 总行数：$(psql_count "SELECT count(*) FROM juhe_dataset.audit_logs WHERE created_at > now() - interval '5 minutes'")" >&2
  echo "    - 容器状态：$(docker compose ps gateway jobs --format '{{.Name}} {{.Status}}' | tr '\n' ' ')" >&2
fi
rm -f "$OUT"

if [ "$FAIL" -eq 0 ]; then echo "== 结果：PASS =="; else echo "== 结果：FAIL ==" >&2; fi
exit "$FAIL"
