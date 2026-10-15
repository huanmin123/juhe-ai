#!/usr/bin/env bash
# 主机层优化自检（幂等，BUG-0313，2026-10-14）——在国内单机生产服务器上执行。
#
# 背景：8G 单机曾因 gateway 流量高峰内存膨胀（RSS 3.56GiB）挤光页缓存引发
# I/O 活锁整机僵死（10-14 11:51-13:48，无 swap 可退、无现场）。本脚本把三项
# 主机层治理固化为可重复执行的幂等步骤，deploy.sh 每次发布自动自检一次，
# 新机初始化也可单独执行：bash host-init.sh（root）。脚本已纳入版本控制，
# 下方路径默认值即生产路径。
#
# 三件套：
#   1. swap 4G（/swapfile，fstab 持久）——内存耗尽兜底，保证有现场不冻结。
#      激活与 fstab 持久化分别判定：本文件处于启用态、激活既有文件、新创建
#      三条路径都核验 fstab 条目（只认非注释行），缺则补写；激活成功但持久
#      化失败同样计 FAIL（重启即丢），激活失败不写 fstab（避免把坏 swap 固
#      化进启动挂载表）；swap 由其他来源启用时不写 fstab（避免悬空挂载项）；
#   2. /etc/sysctl.d/99-juhe-ai.conf——Redis overcommit + 防冻结回收参数。
#      配置文件与 sysctl 运行值分别核对（只查键存在不核对值会让漂移静默
#      存活），漂移即重写配置并重新 sysctl -w，应用后复核；
#   3. journald 容量上限 500M——防日志无限增长挤占磁盘。
#
# 约定：每项"查后再写"（已收敛即 SKIP，不重复改）；宿主机磁盘可用不足 8G 时
# 跳过 swap 创建并告警（不冒险挤占数据盘）；每一步成功与否独立判定，任一环节
# 失败置 FAIL 并以非零退出（deploy.sh 据此仅告警，不阻塞发布）。
#
# 路径可用环境变量覆盖（默认值不变，仅供自动化测试隔离宿主机文件系统）：
#   JUHE_AI_SWAP_FILE（默认 /swapfile）、JUHE_AI_FSTAB_FILE（默认 /etc/fstab）、
#   JUHE_AI_SYSCTL_CONF（默认 /etc/sysctl.d/99-juhe-ai.conf）、
#   JUHE_AI_JOURNALD_CONF（默认 /etc/systemd/journald.conf.d/99-size-cap.conf）
set -uo pipefail

FAIL=0

say() { echo "[host-init] $*"; }

SWAP_FILE=${JUHE_AI_SWAP_FILE:-/swapfile}
FSTAB_FILE=${JUHE_AI_FSTAB_FILE:-/etc/fstab}
SYSCTL_CONF=${JUHE_AI_SYSCTL_CONF:-/etc/sysctl.d/99-juhe-ai.conf}
JOURNALD_CONF=${JUHE_AI_JOURNALD_CONF:-/etc/systemd/journald.conf.d/99-size-cap.conf}

# fstab 持久化：缺条目补写，写入失败独立判 FAIL（swap 已激活但未持久化 =
# 重启即丢，与激活失败同罪）。判定只认非注释行（注释掉的旧条目不算已持久化）。
ensure_swap_fstab_entry() {
  if awk -v swap_file="$SWAP_FILE" '$1 == swap_file { found = 1 } END { exit !found }' "$FSTAB_FILE" 2>/dev/null; then
    say "[SKIP] fstab 已有 $SWAP_FILE 条目"
    return 0
  fi
  if echo "$SWAP_FILE none swap sw 0 0" >> "$FSTAB_FILE" 2>/dev/null; then
    say "[FIX] fstab 补写 $SWAP_FILE 条目"
    return 0
  fi
  say "[WARN] fstab 补写失败（$FSTAB_FILE 不可写）：swap 已启用但重启后不持久"
  FAIL=1
  return 1
}

# --- 1. swap 4G -------------------------------------------------------------
if swapon --show 2>/dev/null | grep -q .; then
  if swapon --show --noheadings 2>/dev/null | grep -q "$SWAP_FILE"; then
    say "[SKIP] swap 已启用: $(swapon --show --noheadings | head -1)"
    ensure_swap_fstab_entry
  else
    # swap 由其他来源（分区/其他文件）启用：防冻结目标已达成，但不得把本
    # 文件条目写进 fstab（本文件不存在时会产生悬空挂载项，重启报失败单元）。
    say "[SKIP] swap 已由其他来源启用（$(swapon --show --noheadings | head -1)），$SWAP_FILE 未参与，不写 fstab"
  fi
elif [ -f "$SWAP_FILE" ]; then
  say "[FIX] 激活既有 $SWAP_FILE"
  if chmod 600 "$SWAP_FILE" && mkswap "$SWAP_FILE" >/dev/null 2>&1 && swapon "$SWAP_FILE" 2>/dev/null; then
    say "[OK] $SWAP_FILE 已激活"
    ensure_swap_fstab_entry
  else
    say "[WARN] swap 激活失败（chmod/mkswap/swapon 任一步失败）"
    FAIL=1
  fi
else
  AVAIL_MB=$(df -m / | awk 'NR==2{print $4}')
  if [ "${AVAIL_MB:-0}" -lt 8192 ]; then
    say "[WARN] 根分区仅剩 ${AVAIL_MB}MB（<8G），跳过 swap 创建；请清理磁盘后重跑"
    FAIL=1
  else
    say "[FIX] 创建 4G swapfile"
    if fallocate -l 4G "$SWAP_FILE" 2>/dev/null && chmod 600 "$SWAP_FILE" \
      && mkswap "$SWAP_FILE" >/dev/null 2>&1 && swapon "$SWAP_FILE" 2>/dev/null; then
      say "[OK] $SWAP_FILE 创建并激活"
      ensure_swap_fstab_entry
    else
      say "[WARN] swap 创建/启用失败（fallocate/chmod/mkswap/swapon 任一步失败）"
      FAIL=1
    fi
  fi
fi

# --- 2. sysctl（/etc/sysctl.d/99-juhe-ai.conf）-------------------------------
# 配置文件写入与运行值分别核对、分别报告；漂移时重写配置并重新应用，应用后复核。
touch "$SYSCTL_CONF" 2>/dev/null || { say "[WARN] 无法创建 $SYSCTL_CONF"; FAIL=1; }
apply_key() {
  key=$1 value=$2
  conf_line=$(grep -E "^${key}[[:space:]]*=" "$SYSCTL_CONF" 2>/dev/null | head -1 || true)
  current=$(sysctl -n "$key" 2>/dev/null || true)
  if [ "$conf_line" = "$key = $value" ] && [ "$current" = "$value" ]; then
    say "[SKIP] sysctl $key = $value（配置与运行值一致）"
    return 0
  fi
  if [ "$conf_line" != "$key = $value" ]; then
    if [ -n "$conf_line" ]; then
      if sed -i -E "s|^${key}[[:space:]]*=.*|$key = $value|" "$SYSCTL_CONF" 2>/dev/null; then
        say "[FIX] sysctl $key 配置已更新为 $value（原：$conf_line）"
      else
        say "[WARN] sysctl $key 配置更新失败（$SYSCTL_CONF 不可写）"
        FAIL=1
      fi
    elif echo "$key = $value" >> "$SYSCTL_CONF" 2>/dev/null; then
      say "[FIX] sysctl $key = $value 已写入配置"
    else
      say "[WARN] sysctl $key 配置写入失败（$SYSCTL_CONF 不可写）"
      FAIL=1
    fi
  fi
  if [ "$current" != "$value" ]; then
    if sysctl -w "$key=$value" >/dev/null 2>&1 && [ "$(sysctl -n "$key" 2>/dev/null || true)" = "$value" ]; then
      say "[FIX] sysctl $key 运行值 ${current:-<读取失败>} -> $value 已应用并复核"
    else
      say "[WARN] sysctl $key 运行值应用/复核失败（当前：${current:-<读取失败>}，期望：$value）"
      FAIL=1
    fi
  fi
}
apply_key vm.overcommit_memory 1        # Redis 后台保存要求（既有契约）
apply_key vm.swappiness 10              # swap 仅作内存耗尽兜底，平时不用
apply_key vm.min_free_kbytes 262144     # 提前启动直接回收，防内存墙 D 状态活锁
apply_key vm.watermark_scale_factor 125 # kswapd 更早更主动，避免突然跌入冻结
sysctl -p "$SYSCTL_CONF" >/dev/null 2>&1 && say "[OK] sysctl 配置整体应用成功" \
  || { say "[WARN] sysctl -p 应用失败"; FAIL=1; }

# --- 3. journald 容量上限 -----------------------------------------------------
JOURNALD_MARKER="SystemMaxUse=500M"
if [ -f "$JOURNALD_CONF" ] && grep -q "$JOURNALD_MARKER" "$JOURNALD_CONF"; then
  say "[SKIP] journald 容量上限已配置"
else
  mkdir -p "$(dirname "$JOURNALD_CONF")" 2>/dev/null || { say "[WARN] 无法创建 $(dirname "$JOURNALD_CONF")"; FAIL=1; }
  if printf '%s\n' "# BUG-0313：限制日志持久化总量（防日志挤占磁盘）" "[Journal]" "$JOURNALD_MARKER" > "$JOURNALD_CONF" 2>/dev/null; then
    if systemctl restart systemd-journald 2>/dev/null; then
      say "[FIX] journald 上限 500M 已写入并重启"
    else
      say "[WARN] journald 配置已写入但重启失败（systemctl restart systemd-journald）"
      FAIL=1
    fi
  else
    say "[WARN] journald 配置写入失败（$JOURNALD_CONF 不可写）"
    FAIL=1
  fi
fi

if [ "$FAIL" -ne 0 ]; then
  say "[FAIL] 存在未收敛项，请按上方 [WARN] 处理后重跑"
  exit 1
fi
say "[OK] 主机层优化全部收敛"
