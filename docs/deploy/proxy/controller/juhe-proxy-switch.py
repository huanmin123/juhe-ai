#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""juhe-proxy-switch: sing-box selector 择优控制器（带迟滞的选节点策略）。

通过 sing-box 的 Clash API 对 selector 组做"先测通再切、防抖、容差择优"：

- 每次运行（建议 systemd timer 每 2 分钟触发）只做有限次探测：
  当前节点 1 次快速测试 + 最多 2 次候选确认测试 + 一批慢扫描。
- 救援切换（唯一绕过最小间隔闸门的路径）：当前节点连续
  FAILED_TIMES_TO_RESCUE 次快速测试失败后，按已知延迟升序对前
  RESCUE_CANDIDATES 个候选做确认测试，第一个通过者当选；全部失败保持不动。
- 性能切换：当前节点正常时，必须同时满足三个闸门才允许切：
  1) 候选已知延迟 + TOLERANCE_MS < 当前延迟（差异过大才切）；
  2) 距上次切换 >= MIN_SWITCH_INTERVAL_S（不频繁切）；
  3) 候选通过确认测试（先测通再切，用贴近真实上游的确认 URL）。
- 失败记忆：节点连续失败 >= FAILED_TIMES_TO_BLACKLIST 次 → 拉黑
  BLACKLIST_COOLDOWN_S，期间不作为候选；冷却到期后由慢扫描重新实测。
- 慢扫描：每轮最多 SWEEP_BATCH 个"记录过期"节点，用快速 URL 刷新延迟基线，
  全池约 SWEEP_BATCH 轮（每轮一个 timer 周期）刷新一遍。
- 状态持久化在 STATE_PATH（失败计数、拉黑、上次切换时间、切换历史），
  控制器重启不失忆。

只依赖 Python 3 标准库。所有读取接口失败都视为对应节点测试失败，
不改变业务行为；脚本自身故障时 selector 停留在当前节点（退化为固定节点）。
"""

import fcntl
import json
import os
import sys
import time
import urllib.parse
import urllib.request

# ---------------------------------------------------------------------------
# 可调参数（策略核心，改动前先读本文档对应章节）
# ---------------------------------------------------------------------------

API_BASE = "http://127.0.0.1:19090"          # sing-box clash_api external_controller
GROUP_TAG = "auto"                            # selector 组 tag（与 sing-box 配置一致）
STATE_DIR = "/var/lib/juhe-proxy-switch"
STATE_PATH = os.path.join(STATE_DIR, "state.json")
LOCK_PATH = os.path.join(STATE_DIR, "lock")

TEST_URL = "https://api.openai.com/v1/models"   # 唯一测试 URL：真实上游，HTTP 401 即视为通
# 说明：该节点池存在"目的地相关"劣化（个别节点 gstatic 通但对部分站点 503），
# 因此排名、健康检查、切换确认统一用真实 AI 上游 URL，不做轻量 URL 快测。
TEST_TIMEOUT_MS = 4000                        # 单次探测超时
HTTP_TIMEOUT_S = 8                            # API 请求整体超时

TOLERANCE_MS = 100                # 性能切换容差：候选必须比当前快超过该值
MIN_SWITCH_INTERVAL_S = 600       # 性能切换最小间隔（救援切换不受限）
FAILED_TIMES_TO_RESCUE = 2        # 当前节点连续失败该次数后触发救援切换
FAILED_TIMES_TO_BLACKLIST = 3     # 连续失败该次数后拉黑
BLACKLIST_COOLDOWN_S = 1800       # 拉黑冷却时长
RESCUE_CANDIDATES = 5             # 救援时最多逐个确认测试的候选数
SWEEP_BATCH = 15                  # 每轮慢扫描节点数上限
SWEEP_STALE_S = 900               # 节点延迟记录超过该时长视为过期（全池约 10 分钟刷新一遍）
HISTORY_LIMIT = 10                # 状态文件里保留的最近切换记录条数


# ---------------------------------------------------------------------------
# Clash API 封装
# ---------------------------------------------------------------------------

def api_get(path, timeout=HTTP_TIMEOUT_S):
    req = urllib.request.Request(API_BASE + path, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def api_put(path, payload, timeout=HTTP_TIMEOUT_S):
    req = urllib.request.Request(
        API_BASE + path,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="PUT",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status


def test_delay(node, url=TEST_URL):
    """对单节点做实时延迟测试；成功返回毫秒数，失败返回 None。"""
    query = urllib.parse.urlencode({"url": url, "timeout": TEST_TIMEOUT_MS})
    try:
        data = api_get("/proxies/%s/delay?%s" % (urllib.parse.quote(node), query),
                       timeout=HTTP_TIMEOUT_S)
    except Exception:
        return None
    delay = data.get("delay")
    if isinstance(delay, (int, float)) and delay > 0:
        return int(delay)
    return None


# ---------------------------------------------------------------------------
# 状态读写
# ---------------------------------------------------------------------------

def load_state():
    try:
        with open(STATE_PATH, "r", encoding="utf-8") as fh:
            state = json.load(fh)
        if isinstance(state, dict) and isinstance(state.get("nodes"), dict):
            return state
    except FileNotFoundError:
        pass
    except Exception as exc:
        log("state-load-error err=%r（按全新状态启动，不影响 sing-box 运行）" % (exc,))
    return {"last_switch_epoch": 0, "nodes": {}, "history": []}


def save_state(state):
    tmp = STATE_PATH + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(state, fh, ensure_ascii=False, indent=1, sort_keys=True)
        fh.write("\n")
    os.replace(tmp, STATE_PATH)


def node_rec(state, name, now):
    rec = state["nodes"].setdefault(name, {})
    rec.setdefault("delay", None)
    rec.setdefault("fails", 0)
    rec.setdefault("tested_at", 0)
    rec.setdefault("blacklist_until", 0)
    return rec


def blacklisted(rec, now):
    return rec["blacklist_until"] > now


# ---------------------------------------------------------------------------
# 动作
# ---------------------------------------------------------------------------

def switch_to(state, group, target, reason, now):
    api_put("/proxies/%s" % GROUP_TAG, {"name": target})
    after = api_get("/proxies/%s" % GROUP_TAG)
    if after.get("now") != target:
        raise RuntimeError("switch not effective: now=%r want=%r" % (after.get("now"), target))
    state["last_switch_epoch"] = now
    state.setdefault("history", []).insert(
        0, {"at": now, "from": group.get("now"), "to": target, "reason": reason})
    del state["history"][HISTORY_LIMIT:]
    log("switch reason=%s from=%s to=%s" % (reason, group.get("now"), target))


def rescue(state, group, all_nodes, now):
    """当前节点连续失败：按已知延迟升序逐个确认测试，第一个通过者当选。"""
    known = []
    unknown = []
    for name in all_nodes:
        if name == group.get("now"):
            continue
        rec = state["nodes"].get(name)
        if rec and blacklisted(rec, now):
            continue
        if rec and rec["delay"]:
            known.append((rec["delay"], name))
        else:
            unknown.append(name)
    known.sort()
    candidates = [name for _, name in known[:RESCUE_CANDIDATES]]
    if len(candidates) < RESCUE_CANDIDATES:
        candidates += unknown[: RESCUE_CANDIDATES - len(candidates)]
    for cand in candidates:
        delay = test_delay(cand)
        if delay is None:
            node_rec(state, cand, now)["fails"] += 1
            log("rescue-confirm-fail cand=%s" % cand)
            continue
        rec = node_rec(state, cand, now)
        rec.update(delay=delay, fails=0, tested_at=now)
        switch_to(state, group, cand, "rescue", now)
        return True
    log("rescue-exhausted candidates=%d 保持当前节点" % len(candidates))
    return False


def performance(state, group, all_nodes, now, current_delay):
    """三闸门性能切换：容差 + 最小间隔 + 确认测试。"""
    if now - state.get("last_switch_epoch", 0) < MIN_SWITCH_INTERVAL_S:
        return False
    ranked = []
    for name in all_nodes:
        if name == group.get("now"):
            continue
        rec = state["nodes"].get(name)
        if not rec or not rec["delay"] or blacklisted(rec, now):
            continue
        if rec["delay"] + TOLERANCE_MS < current_delay:
            ranked.append((rec["delay"], name))
    ranked.sort()
    for _, cand in ranked[:2]:
        delay = test_delay(cand)
        if delay is None:
            node_rec(state, cand, now)["fails"] += 1
            log("perf-confirm-fail cand=%s" % cand)
            continue
        rec = node_rec(state, cand, now)
        rec.update(delay=delay, fails=0, tested_at=now)
        switch_to(state, group, cand, "performance", now)
        return True
    return False


def sweep(state, all_nodes, now):
    """批量刷新过期节点的延迟基线（拉黑中的节点跳过，冷却后自然过期重测）。"""
    stale = []
    for name in all_nodes:
        rec = state["nodes"].get(name)
        if rec and blacklisted(rec, now):
            continue
        if not rec or now - rec["tested_at"] > SWEEP_STALE_S:
            stale.append((rec["tested_at"] if rec else 0, name))
    stale.sort()
    ok = fail = 0
    for _, name in stale[:SWEEP_BATCH]:
        delay = test_delay(name)
        rec = node_rec(state, name, now)
        if delay is None:
            rec["fails"] += 1
            rec["tested_at"] = now
            if rec["fails"] >= FAILED_TIMES_TO_BLACKLIST:
                rec["blacklist_until"] = now + BLACKLIST_COOLDOWN_S
                log("blacklist node=%s fails=%d cooldown_s=%d"
                    % (name, rec["fails"], BLACKLIST_COOLDOWN_S))
            fail += 1
        else:
            rec.update(delay=delay, fails=0, tested_at=now, blacklist_until=0)
            ok += 1
    return ok, fail


# ---------------------------------------------------------------------------

def log(message):
    print(time.strftime("%Y-%m-%dT%H:%M:%S%z "), message, flush=True)


def main():
    os.makedirs(STATE_DIR, exist_ok=True)
    lock = open(LOCK_PATH, "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        log("another run in progress, skip")
        return 0

    group = api_get("/proxies/%s" % GROUP_TAG)
    if group.get("type") != "Selector":
        log("group %s type=%s 非 Selector（sing-box 配置未迁移？），控制器拒绝动作"
            % (GROUP_TAG, group.get("type")))
        return 2
    all_nodes = [n for n in group.get("all", []) if n not in ("DIRECT", "REJECT")]
    current = group.get("now")
    if current not in all_nodes:
        log("current %r 不在候选列表，跳过本轮" % (current,))
        return 2

    state = load_state()
    now = int(time.time())
    rec = node_rec(state, current, now)

    current_delay = test_delay(current)
    if current_delay is None:
        rec["fails"] += 1
        rec["tested_at"] = now
        acted = False
        if rec["fails"] >= FAILED_TIMES_TO_RESCUE:
            acted = rescue(state, group, all_nodes, now)
        summary = "current-fail node=%s fails=%d acted=%s" % (current, rec["fails"], acted)
    else:
        rec.update(delay=current_delay, fails=0, tested_at=now, blacklist_until=0)
        sweep(state, all_nodes, now)
        acted = performance(state, group, all_nodes, now, current_delay)
        summary = "current-ok node=%s delay=%d acted=%s" % (current, current_delay, acted)

    save_state(state)
    known = sum(1 for r in state["nodes"].values() if r.get("delay"))
    banned = sum(1 for r in state["nodes"].values() if blacklisted(r, now))
    log("%s known=%d blacklisted=%d" % (summary, known, banned))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # 兜底：控制器异常不允许影响代理本身
        log("fatal err=%r" % (exc,))
        sys.exit(1)
