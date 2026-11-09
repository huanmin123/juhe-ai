#!/usr/bin/env bash
# 发布后验证脚本：在目标服务器 compose 目录（如 /opt/juhe-ai）下运行；
# deploy.sh 第 [6/6] 步自动上传并调用，也可手动运行。
#
# 检查项（均为无凭据强制项；原第 3 项"业务闭环（/v1 请求 → 审计+用量落库）"
# 已于 2026-10-03 按用户裁定移除——闭环依赖具体账户/模型的实时可用性，
# 无法保证稳定，作为强制门禁会随上游波动误报；链路落库正确性改由发布后
# 巡检（audit_logs/usage_records 新行字段核验）覆盖）：
#   1. gateway 内存限制断言（BUG-0313）：容器 env 必须含 GOMEMLIMIT 且
#      mem_limit 非 0——双限缺一，流量高峰 RSS 会无封顶膨胀、挤光页缓存
#      引发整机 I/O 活锁（10-14 事故）。只断言"已配置"，不锁定具体数值
#      （数值调整属 compose 契约变更，随 deploy.sh 配置同步下发）。
#
# 2026-11-09 T188：usage-record-spool 交接退役（gateway 直投 PG），原
# spool 同源断言与 jobs drain 接线断言一并删除。
#
# 容器内端口拓扑（实测 2026-10-03）：3000=业务链路（/v1/*），3306=health+metrics
# 专用（compose healthcheck；无 /v1 路由）。gateway 3306 / jobs 3305 另经宿主
# 回环端口映射发布（127.0.0.1:3306:3306 / 127.0.0.1:3305:3305），仅宿主回环可达，
# 供 infra 机 Prometheus 经 SSH 隧道抓取（部署层拒绝公网入口，见 monitoring/README.md）。
#
# 用法：cd <compose 目录> && bash verify-release.sh
# 退出码：0 = 全部通过；1 = 任一强制项失败。
set -euo pipefail

cd "$(dirname "$0")"
COMPOSE_DIR=$(pwd)
GW_CONTAINER=juhe-ai-go-gateway
FAIL=0

fail() { echo "  [FAIL] $*" >&2; FAIL=1; }
pass() { echo "  [PASS] $*"; }

echo "== 发布后验证（compose 目录：$COMPOSE_DIR）=="

# ---------------------------------------------------------------------------
# 1. gateway 内存限制断言（BUG-0313）：GOMEMLIMIT 软限 + mem_limit 硬限缺一，
#    流量高峰 RSS 会无封顶膨胀、挤光页缓存引发整机 I/O 活锁（10-14 事故）。
#    compose 契约：GOMEMLIMIT=2300MiB、mem_limit 3g；此处只断言"已配置"
#    （存在且非 0），不锁定具体数值——数值调整属 compose 契约变更，随
#    deploy.sh 配置同步机制下发，验证只防"契约丢失"。
# ---------------------------------------------------------------------------
echo "-- [1/1] gateway 内存限制断言（BUG-0313）"
GW_MEMLIMIT_BYTES=$(docker inspect "$GW_CONTAINER" --format '{{.HostConfig.Memory}}')
if [ -n "$GW_MEMLIMIT_BYTES" ] && [ "$GW_MEMLIMIT_BYTES" != "0" ]; then
  pass "gateway mem_limit 已配置（${GW_MEMLIMIT_BYTES} bytes）"
else
  fail "gateway mem_limit 未配置（HostConfig.Memory=0）：内存耗尽会拖死整机而非只 OOM 杀 gateway 容器自身"
fi
GW_GOMEMLIMIT=$(docker exec "$GW_CONTAINER" printenv GOMEMLIMIT 2>/dev/null || true)
if [ -n "$GW_GOMEMLIMIT" ]; then
  pass "gateway 容器 env 含 GOMEMLIMIT=$GW_GOMEMLIMIT"
else
  fail "gateway 容器 env 缺 GOMEMLIMIT：Go 堆软限制缺失，RSS 无封顶膨胀"
fi

if [ "$FAIL" -eq 0 ]; then echo "== 结果：PASS =="; else echo "== 结果：FAIL ==" >&2; fi
exit "$FAIL"
