#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""juhe-sub-update: sing-box 订阅定时更新器（通用版，云端/家庭同一份代码）。

每小时拉取订阅并做变更检测，节点有变化才平滑换血：

1. 按 FETCH_VIA 抓取链依次尝试（直连 → 各本机 socks 出口），第一个成功者生效。
2. base64 解码分享链接，解析为 sing-box outbounds（vless ws+tls /
   vless reality+vision / hysteria2+pin；过滤"剩余流量/套餐到期"等数值型
   信息条目——它们常变，过滤后变更检测才稳定）。
3. 与线上节点做无序集合比较：一致 → 零动作退出，不打断在途连接。
4. 有变化：备份（保留 BACKUP_KEEP 份）→ `sing-box check` 门禁（不过则放弃，
   线上配置分毫不动）→ 安装并重启服务 → 健康门禁（clash_api 或本地 socks
   实测，不过则自动回滚上一版并重启）→ 重置择优控制器状态（如部署）
   → 经 clash_api 抽样预选健康节点（如部署）。

部署差异全部通过环境变量注入（见 systemd unit），只依赖 Python 3 标准库与 curl。
"""

import fcntl
import glob
import json
import os
import subprocess
import sys
import time
import urllib.parse
import urllib.request

# ---------------------------------------------------------------------------
# 部署差异（环境变量注入，缺省值为云端单机形态）
# ---------------------------------------------------------------------------

CONFIG_PATH = "/etc/sing-box/config.json"
SUB_URL_FILE = "/etc/sing-box/subscription-url"
BACKUP_GLOB = "/etc/sing-box/config.json.bak-*"
BACKUP_KEEP = 5
LOCK_PATH = "/var/lib/juhe-sub-update/lock"
LOG_PATH = "/var/log/juhe-sub-update.log"

SINGBOX_BIN = os.environ.get("JUHE_SB_BIN", "/usr/local/bin/sing-box")
RESTART_UNIT = os.environ.get("JUHE_SB_UNIT", "juhe-pw-proxy")
# clash_api 地址；空串 = 该部署没有 clash_api（家庭形态），健康门禁走本地 socks 实测
CLASH_API = os.environ.get("JUHE_SB_API", "http://127.0.0.1:19090")
GROUP_TAG = os.environ.get("JUHE_SB_GROUP", "auto")
# 重启后的本地 socks 实测出口（两态都做这道门禁）
LOCAL_SOCKS_TEST = os.environ.get("JUHE_SB_SOCKS_TEST", "socks5h://172.18.0.1:17892")
# 择优控制器状态文件（存在才重置；家庭无控制器，天然跳过）
CONTROLLER_STATE = os.environ.get(
    "JUHE_SB_CONTROLLER_STATE", "/var/lib/juhe-proxy-switch/state.json")
# 抓取链：逗号分隔，direct（或空）表示直连，其余为 socks5h://host:port；按序取第一个成功者
FETCH_VIA = [None if v.strip() in ("", "direct") else v.strip()
             for v in os.environ.get(
                 "JUHE_SB_FETCH_VIA",
                 "direct,socks5h://127.0.0.1:17890,socks5h://172.18.0.1:17892").split(",")]

TEST_URL = "https://api.openai.com/v1/models"   # 预选/健康与控制器同一把尺子
PROBE_URL = "https://www.gstatic.com/generate_204"  # 无 clash_api 时的轻量探针
TEST_TIMEOUT_MS = 4000
PRESELECT_SAMPLES = 10
FETCH_TIMEOUT_S = 30

NODE_TYPES = ("vless", "hysteria2")
INFO_NAME_PATTERNS = ("剩余流量", "套餐到期", "官网", "到期", "重置")


def log(message):
    print("%s %s" % (time.strftime("%Y-%m-%dT%H:%M:%S%z "), message), flush=True)


# ---------------------------------------------------------------------------
# 抓取与解析
# ---------------------------------------------------------------------------

def looks_like_subscription(raw):
    """面板限频/错误页（HTTP 403 等）也会 200 返回，须校验内容形态。"""
    import base64
    try:
        txt = base64.b64decode(raw + b"=" * (-len(raw) % 4)).decode("utf-8", "replace")
    except Exception:
        return False
    return "://" in txt[:4096]


def fetch_subscription(url):
    # 抓取链整体重试两轮：面板对同出口高频抓取会返回限频页（约 10 秒解封）
    for attempt in range(2):
        if attempt:
            log("retry fetch chain after 30s (rate-limit page suspected)")
            time.sleep(30)
        last = None
        for via in FETCH_VIA:
            cmd = ["curl", "-sS", "--max-time", str(FETCH_TIMEOUT_S)]
            if via:
                cmd += ["-x", via]
            cmd += [url]
            try:
                proc = subprocess.run(cmd, capture_output=True, timeout=FETCH_TIMEOUT_S + 10)
            except subprocess.TimeoutExpired:
                last = (via or "direct", "timeout")
                continue
            if proc.returncode == 0 and proc.stdout:
                if looks_like_subscription(proc.stdout):
                    log("fetch ok via %s, %d bytes" % (via or "direct", len(proc.stdout)))
                    return proc.stdout
                last = (via or "direct", "non-subscription body (%d bytes, "
                        "likely rate-limit page)" % len(proc.stdout))
                log("fetch fail via %s: %s" % last)
                continue
            last = (via or "direct", proc.stderr.decode("utf-8", "replace").strip()[:100])
            log("fetch fail via %s: %s" % last)
    raise RuntimeError("all fetch paths failed (%r)" % (last,))


def parse_nodes(raw):
    import base64
    txt = base64.b64decode(raw + b"=" * (-len(raw) % 4)).decode("utf-8", "replace")
    nodes = []
    for link in (l.strip() for l in txt.splitlines() if l.strip()):
        scheme, _, rest = link.partition("://")
        frag = rest.split("#", 1)[1] if "#" in rest else ""
        if any(p in urllib.parse.unquote(frag) for p in INFO_NAME_PATTERNS):
            continue
        body = rest.split("#", 1)[0]
        if "@" not in body:
            continue
        userinfo, _, hostport = body.partition("@")
        hostpart, _, qstr = hostport.partition("?")
        q = dict(urllib.parse.parse_qsl(qstr))
        host, _, port = hostpart.rpartition(":")
        host, port = host.rstrip("/"), int(port.rstrip("/"))
        if scheme == "vless":
            node = {"type": "vless", "server": host, "server_port": port,
                    "uuid": urllib.parse.unquote(userinfo)}
            tls = {"enabled": True, "server_name": q.get("sni") or q.get("host") or host}
            if q.get("fp"):
                tls["utls"] = {"enabled": True, "fingerprint": q["fp"]}
            if q.get("pbk"):
                tls["reality"] = {"enabled": True, "public_key": q["pbk"],
                                  "short_id": q.get("sid", "")}
            if q.get("insecure") == "1":
                tls["insecure"] = True
            node["tls"] = tls
            if q.get("flow"):
                node["flow"] = q["flow"]
            transport = q.get("type", "tcp")
            if transport == "ws":
                tr = {"type": "ws", "path": urllib.parse.unquote(q.get("path", "/"))}
                if q.get("host"):
                    tr["headers"] = {"Host": q["host"]}
                node["transport"] = tr
            elif transport != "tcp":
                raise RuntimeError("unhandled vless transport %r" % transport)
            nodes.append(node)
        elif scheme == "hysteria2":
            node = {"type": "hysteria2", "server": host, "server_port": port,
                    "password": urllib.parse.unquote(userinfo)}
            tls = {"enabled": True, "server_name": q.get("sni") or host}
            if q.get("pinSHA256"):
                tls["certificate_public_key_sha256"] = [q["pinSHA256"]]
            if q.get("insecure") in ("1", "true"):
                tls["insecure"] = True
            node["tls"] = tls
            if q.get("mport"):
                node["server_ports"] = [q["mport"].replace("-", ":")]
            nodes.append(node)
        else:
            raise RuntimeError("unhandled scheme %r" % scheme)
    return nodes


def node_signature(node):
    return json.dumps(node, ensure_ascii=False, sort_keys=True)


def current_signatures(config):
    return {node_signature({k: v for k, v in o.items() if k != "tag"})
            for o in config["outbounds"] if o.get("type") in NODE_TYPES}


# ---------------------------------------------------------------------------
# 组装与应用
# ---------------------------------------------------------------------------

def build_config(config, nodes):
    """重编节点 tag，并把引用了旧节点集合的所有 selector/urltest 组换成新列表。

    节点插回位置：紧跟第一个分组之后（兼容云端 [入口组,节点,直连] 与家庭
    [直连,阻断,...节点,...多个 urltest 组] 两种布局）。
    """
    for i, node in enumerate(nodes, 1):
        node["tag"] = "sub-%03d" % i
    new_tags = [o["tag"] for o in nodes]
    old_tags = {o["tag"] for o in config["outbounds"] if o.get("type") in NODE_TYPES}
    kept, group_seen_at = [], None
    for o in config["outbounds"]:
        if o.get("type") in NODE_TYPES:
            continue
        if o.get("type") in ("selector", "urltest") and old_tags \
                and set(o.get("outbounds", [])) == old_tags:
            o["outbounds"] = list(new_tags)
            if o.get("type") == "selector" and o.get("default") not in new_tags:
                o["default"] = new_tags[0]
            group_seen_at = len(kept)
        kept.append(o)
    if group_seen_at is None:
        group_seen_at = len(kept) - 1
    config["outbounds"] = kept[:group_seen_at + 1] + nodes + kept[group_seen_at + 1:]
    return config


def api_call(path, payload=None, method=None, timeout=8):
    req = urllib.request.Request(CLASH_API + path,
                                 method=method or ("GET" if payload is None else "PUT"))
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, data=data, timeout=timeout) as resp:
        body = resp.read().decode("utf-8")
    return json.loads(body) if body.strip() else None


def test_delay(tag):
    q = urllib.parse.urlencode({"url": TEST_URL, "timeout": TEST_TIMEOUT_MS})
    try:
        data = api_call("/proxies/%s/delay?%s" % (urllib.parse.quote(tag), q))
    except Exception:
        return None
    delay = data.get("delay")
    return int(delay) if isinstance(delay, (int, float)) and delay > 0 else None


def service_healthy():
    """重启后健康门禁：优先 clash_api，再叠加本地 socks 实测；任一可用即通过。"""
    api_ok = socks_ok = None
    if CLASH_API:
        for _ in range(5):
            time.sleep(2)
            try:
                if api_call("/proxies/%s" % GROUP_TAG).get("type") in ("Selector", "URLTest"):
                    api_ok = True
                    break
            except Exception:
                pass
        if api_ok is None:
            return False
    for _ in range(5):
        time.sleep(2)
        proc = subprocess.run(
            ["curl", "-sS", "--max-time", "10", "-x", LOCAL_SOCKS_TEST,
             "-o", "/dev/null", "-w", "%{http_code}", PROBE_URL],
            capture_output=True, timeout=20)
        if proc.stdout.decode().strip() in ("204", "200"):
            socks_ok = True
            break
    return True if (api_ok or socks_ok) else False


def rollback():
    backups = sorted(glob.glob(BACKUP_GLOB))
    if not backups:
        return
    subprocess.run(["cp", "-a", backups[-1], CONFIG_PATH], check=True)
    subprocess.run(["systemctl", "restart", RESTART_UNIT], check=True)
    log("rolled back to %s" % backups[-1])


def prune_backups():
    for path in sorted(glob.glob(BACKUP_GLOB))[:-BACKUP_KEEP]:
        os.remove(path)


def main():
    os.makedirs(os.path.dirname(LOCK_PATH), exist_ok=True)
    lock = open(LOCK_PATH, "a")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        log("another run in progress, skip")
        return 0

    url = open(SUB_URL_FILE).read().strip()
    nodes = parse_nodes(fetch_subscription(url))
    if not nodes:
        raise RuntimeError("parsed 0 nodes, keep current config")
    log("parsed %d nodes from subscription" % len(nodes))

    config = json.load(open(CONFIG_PATH, encoding="utf-8"))
    if current_signatures(config) == {node_signature(n) for n in nodes}:
        log("unchanged (nodes=%d), no restart" % len(nodes))
        return 0

    stamp = time.strftime("%Y%m%d-%H%M%S")
    backup = "/etc/sing-box/config.json.bak-" + stamp
    subprocess.run(["cp", "-a", CONFIG_PATH, backup], check=True)
    prune_backups()

    new_config = build_config(config, json.loads(json.dumps(nodes)))
    candidate = CONFIG_PATH + ".candidate"
    with open(candidate, "w", encoding="utf-8") as fh:
        json.dump(new_config, fh, ensure_ascii=False, indent=2)
    check = subprocess.run([SINGBOX_BIN, "check", "-c", candidate], capture_output=True)
    if check.returncode != 0:
        os.remove(candidate)
        raise RuntimeError("sing-box check failed: %s"
                           % check.stderr.decode("utf-8", "replace")[:300])

    os.replace(candidate, CONFIG_PATH)
    subprocess.run(["systemctl", "restart", RESTART_UNIT], check=True)
    if not service_healthy():
        rollback()
        raise RuntimeError("service unhealthy after restart, rolled back")
    log("applied %d nodes, backup=%s" % (len(nodes), backup))

    if os.path.exists(CONTROLLER_STATE):
        os.remove(CONTROLLER_STATE)   # 择优控制器的节点记录已失效，由其重新学习
        log("controller state reset")

    if CLASH_API:
        applied_tags = [o["tag"] for o in new_config["outbounds"]
                        if o.get("type") in NODE_TYPES]
        step = max(1, len(applied_tags) // PRESELECT_SAMPLES)
        healthy = None
        for tag in applied_tags[::step][:PRESELECT_SAMPLES]:
            delay = test_delay(tag)
            log("preselect %s -> %s" % (tag, delay if delay else "fail"))
            if delay and not healthy:
                healthy = tag
        if healthy:
            api_call("/proxies/%s" % GROUP_TAG, {"name": healthy}, method="PUT")
            log("selector preselected -> %s" % healthy)
        else:
            log("WARNING: no healthy node in preselect sample; selector stays default")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        try:
            if "unhealthy after restart" in str(exc):
                rollback()
        except Exception as rollback_exc:
            log("rollback failed: %r" % (rollback_exc,))
        log("fatal err=%r" % (exc,))
        sys.exit(1)
