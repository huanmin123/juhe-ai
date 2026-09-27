#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""juhe-proxy-switch: sing-box selector 择优控制器（多组模式，云端/家庭同一份代码）。

通过 sing-box 的 Clash API 对一个或多个 selector 组做"先测通再切、防抖、
容差择优"：

- 每次运行（建议 systemd timer 每 2 分钟触发）按组独立评估：各组当前节点
  用【该组自己的测试 URL】做健康检查（保留按服务分流的语义）。
- 救援切换（唯一绕过最小间隔闸门的路径）：某组当前节点连续
  FAILED_TIMES_TO_RESCUE 次失败后，按该组已知延迟升序对候选做确认测试，
  第一个通过者当选；全部失败保持不动。
- 性能切换：当前节点正常时，必须同时满足三个闸门：
  1) 候选延迟 + TOLERANCE_MS < 当前延迟（差异过大才切）；
  2) 距该组上次切换 >= MIN_SWITCH_INTERVAL_S（不频繁切，按组独立计时）；
  3) 候选通过该组测试 URL 的确认测试（先测通再切）。
- 失败记忆按"组 × 节点"独立记账（不同服务目的地可用性不同，不能混用）：
  连续失败 >= FAILED_TIMES_TO_BLACKLIST 次 → 该组内拉黑 BLACKLIST_COOLDOWN_S。
- 慢扫描：每轮总预算 SWEEP_BATCH 个节点均摊到各组，用各组自己的测试 URL
  刷新该组视角的延迟基线。
- 状态持久化（按组分桶），控制器重启不失忆；脚本自身故障时 selector
  停留在当前节点，不影响 sing-box 运行。

部署差异经 systemd unit 环境变量注入（JUHE_SB_*），见部署指南第 10 节。
只依赖 Python 3 标准库。
"""

import fcntl
import json
import os
import sys
import time
import urllib.parse
import urllib.request

# ---------------------------------------------------------------------------
# 可调参数（策略核心，改动前先读部署指南对应章节）
# ---------------------------------------------------------------------------

API_BASE = os.environ.get("JUHE_SB_API", "http://127.0.0.1:19090")
# 管理的 selector 组，逗号分隔；云端单组 auto，家庭四组分流
GROUPS = [g.strip() for g in
          os.environ.get("JUHE_SB_GROUPS", "auto").split(",") if g.strip()]
# 各组测试 URL，格式 组=URL,组=URL；未列出的组用 TEST_URL
GROUP_TEST_URLS = dict(
    kv.split("=", 1) for kv in
    os.environ.get("JUHE_SB_GROUP_URLS", "").split(",") if "=" in kv)
TEST_URL = os.environ.get("JUHE_SB_TEST_URL", "https://api.openai.com/v1/models")

STATE_DIR = os.environ.get("JUHE_SB_STATE_DIR", "/var/lib/juhe-proxy-switch")
STATE_PATH = os.path.join(STATE_DIR, "state.json")
LOCK_PATH = os.path.join(STATE_DIR, "lock")

TOLERANCE_MS = 100                # 性能切换容差：候选必须比当前快超过该值
MIN_SWITCH_INTERVAL_S = 600       # 性能切换最小间隔（救援切换不受限，按组计时）
FAILED_TIMES_TO_RESCUE = 2        # 当前节点连续失败该次数后触发救援切换
FAILED_TIMES_TO_BLACKLIST = 3     # 连续失败该次数后拉黑（组内）
BLACKLIST_COOLDOWN_S = 1800       # 拉黑冷却时长
RESCUE_CANDIDATES = 5             # 救援时最多逐个确认测试的候选数
SWEEP_BATCH = 15                  # 每轮慢扫描总预算（均摊到各组）
SWEEP_STALE_S = 900               # 节点延迟记录超过该时长视为过期
HISTORY_LIMIT = 30                # 状态文件里保留的最近切换记录条数


def group_test_url(group):
    return GROUP_TEST_URLS.get(group, TEST_URL)


# ---------------------------------------------------------------------------
# Clash API 封装
# ---------------------------------------------------------------------------

def api_get(path, timeout=8):
    req = urllib.request.Request(API_BASE + path, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def api_put(path, payload, timeout=8):
    req = urllib.request.Request(
        API_BASE + path,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="PUT",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status


def test_delay(node, url):
    """对单节点做实时延迟测试；成功返回毫秒数，失败返回 None。"""
    query = urllib.parse.urlencode({"url": url, "timeout": 4000})
    try:
        data = api_get("/proxies/%s/delay?%s" % (urllib.parse.quote(node), query))
    except Exception:
        return None
    delay = data.get("delay")
    return int(delay) if isinstance(delay, (int, float)) and delay > 0 else None


# ---------------------------------------------------------------------------
# 状态（节点记录按组分桶；健康计数与拉黑组内独立）
# ---------------------------------------------------------------------------

def load_state(groups):
    try:
        with open(STATE_PATH, "r", encoding="utf-8") as fh:
            state = json.load(fh)
        if isinstance(state.get("groups"), dict) and isinstance(state.get("nodes"), dict):
            return state
    except FileNotFoundError:
        pass
    except Exception as exc:
        log("state-load-error err=%r（按全新状态启动，不影响 sing-box 运行）" % (exc,))
    return {"groups": {g: {"fails": 0, "last_switch": 0} for g in groups},
            "nodes": {g: {} for g in groups}, "history": []}


def save_state(state):
    tmp = STATE_PATH + ".tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        json.dump(state, fh, ensure_ascii=False, indent=1, sort_keys=True)
        fh.write("\n")
    os.replace(tmp, STATE_PATH)


def node_rec(state, group, name, now):
    rec = state["nodes"].setdefault(group, {}).setdefault(name, {})
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

def switch_to(state, group, current, target, reason, now):
    api_put("/proxies/%s" % urllib.parse.quote(group), {"name": target})
    after = api_get("/proxies/%s" % urllib.parse.quote(group))
    if after.get("now") != target:
        raise RuntimeError("switch not effective: now=%r want=%r" % (after.get("now"), target))
    state["groups"].setdefault(group, {})["last_switch"] = now
    state.setdefault("history", []).insert(
        0, {"at": now, "group": group, "from": current, "to": target, "reason": reason})
    del state["history"][HISTORY_LIMIT:]
    log("switch group=%s reason=%s from=%s to=%s" % (group, reason, current, target))


def rescue(state, group, all_nodes, current, now, url):
    """当前组节点连续失败：按已知延迟升序逐个确认测试，第一个通过者当选。"""
    known, unknown = [], []
    for name in all_nodes:
        if name == current:
            continue
        rec = state["nodes"].get(group, {}).get(name)
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
        delay = test_delay(cand, url)
        if delay is None:
            node_rec(state, group, cand, now)["fails"] += 1
            log("rescue-confirm-fail group=%s cand=%s" % (group, cand))
            continue
        rec = node_rec(state, group, cand, now)
        rec.update(delay=delay, fails=0, tested_at=now)
        switch_to(state, group, current, cand, "rescue", now)
        return True
    log("rescue-exhausted group=%s candidates=%d 保持当前节点" % (group, len(candidates)))
    return False


def performance(state, group, all_nodes, current, now, current_delay, url):
    """三闸门性能切换：容差 + 按组最小间隔 + 确认测试。"""
    if now - state["groups"].get(group, {}).get("last_switch", 0) < MIN_SWITCH_INTERVAL_S:
        return False
    ranked = []
    for name in all_nodes:
        if name == current:
            continue
        rec = state["nodes"].get(group, {}).get(name)
        if not rec or not rec["delay"] or blacklisted(rec, now):
            continue
        if rec["delay"] + TOLERANCE_MS < current_delay:
            ranked.append((rec["delay"], name))
    ranked.sort()
    for _, cand in ranked[:2]:
        delay = test_delay(cand, url)
        if delay is None:
            node_rec(state, group, cand, now)["fails"] += 1
            log("perf-confirm-fail group=%s cand=%s" % (group, cand))
            continue
        rec = node_rec(state, group, cand, now)
        rec.update(delay=delay, fails=0, tested_at=now)
        switch_to(state, group, current, cand, "performance", now)
        return True
    return False


def sweep(state, all_nodes, now):
    """批量刷新过期节点的延迟基线：总预算均摊到各组，用各组自己的测试 URL。"""
    budget = max(4, SWEEP_BATCH // max(1, len(GROUPS)))
    for group in GROUPS:
        url = group_test_url(group)
        stale = []
        for name in all_nodes:
            rec = state["nodes"].get(group, {}).get(name)
            if rec and blacklisted(rec, now):
                continue
            if not rec or now - rec["tested_at"] > SWEEP_STALE_S:
                stale.append((rec["tested_at"] if rec else 0, name))
        stale.sort()
        for _, name in stale[:budget]:
            delay = test_delay(name, url)
            rec = node_rec(state, group, name, now)
            if delay is None:
                rec["fails"] += 1
                rec["tested_at"] = now
                if rec["fails"] >= FAILED_TIMES_TO_BLACKLIST:
                    rec["blacklist_until"] = now + BLACKLIST_COOLDOWN_S
                    log("blacklist group=%s node=%s fails=%d"
                        % (group, name, rec["fails"]))
            else:
                rec.update(delay=delay, fails=0, tested_at=now, blacklist_until=0)


# ---------------------------------------------------------------------------

def log(message):
    print(time.strftime("%Y-%m-%dT%H:%M:%S%z "), message, flush=True)


def main():
    os.makedirs(STATE_DIR, exist_ok=True)
    lock = open(LOCK_PATH, "a")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        log("another run in progress, skip")
        return 0

    # 各组必须是 Selector 且引用同一节点池（否则无法用统一的全池扫描服务所有组）
    api_now, all_nodes = {}, None
    for group in GROUPS:
        info = api_get("/proxies/%s" % urllib.parse.quote(group))
        if info.get("type") != "Selector":
            log("group %s type=%s 非 Selector（sing-box 配置未迁移？），控制器拒绝动作"
                % (group, info.get("type")))
            return 2
        api_now[group] = info.get("now")
        nodes = [n for n in info.get("all", []) if n not in ("DIRECT", "REJECT")]
        if all_nodes is None:
            all_nodes = nodes
        elif nodes != all_nodes:
            log("group %s 节点列表与其他组不一致，跳过本轮" % group)
            return 2

    state = load_state(GROUPS)
    for g in GROUPS:   # 组列表变化（如部署调整）时补齐，旧组数据保留无害
        state["groups"].setdefault(g, {"fails": 0, "last_switch": 0})
        state["nodes"].setdefault(g, {})
    now = int(time.time())

    healthy_groups = []
    for group in GROUPS:
        url = group_test_url(group)
        current = api_now[group]
        rec = node_rec(state, group, current, now)
        delay = test_delay(current, url)
        if delay is None:
            rec["fails"] += 1
            rec["tested_at"] = now
            acted = False
            if rec["fails"] >= FAILED_TIMES_TO_RESCUE:
                acted = rescue(state, group, all_nodes, current, now, url)
            log("group=%s current-fail node=%s fails=%d acted=%s"
                % (group, current, rec["fails"], acted))
        else:
            rec.update(delay=delay, fails=0, tested_at=now, blacklist_until=0)
            healthy_groups.append((group, current, delay))
            log("group=%s current-ok node=%s delay=%d" % (group, current, delay))

    # 健康组才进入性能切换（故障组本轮由救援负责）；全池扫描刷新各视角基线
    sweep(state, all_nodes, now)
    for group, current, delay in healthy_groups:
        performance(state, group, all_nodes, current, now, delay, group_test_url(group))

    save_state(state)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # 兜底：控制器异常不允许影响代理本身
        log("fatal err=%r" % (exc,))
        sys.exit(1)
