#!/usr/bin/env python3
"""Redis 慢命令快照：cron 每 5 分钟增量导出 SLOWLOG 到 jsonl + node-exporter textfile 指标。

用途：SLOWLOG 是内存环形缓冲（应用 compose 已配 --slowlog-max-len 1024、阈值默认 10ms），
Redis 重启即清空；本脚本按 entry id 增量落盘实现持久追溯（检测到 id 回绕=Redis 重启，
自动重置基线全量重导）。

产物：
- /opt/juhe-monitoring/redis-slowlog/slowlog.jsonl
  每行一条：id / ts / us / cmd / argv / client / cname
  超过 10MB gzip 轮转，保留 14 天。
- /opt/juhe-monitoring/textfile/redis_slowlog.prom
  node-exporter textfile 指标 juhe_redis_slowlog_*（告警数据源）。

依赖：宿主机可执行 docker，容器名与应用 compose 一致（juhe-ai-redis）。
已知边界：两次运行间隔内被环形缓冲挤出的条目（5 分钟内新增 >1024 条）会缺失。
"""
import glob
import gzip
import json
import os
import subprocess
import sys
import time

BASE = "/opt/juhe-monitoring/redis-slowlog"
PROM_DIR = "/opt/juhe-monitoring/textfile"
STATE = os.path.join(BASE, "state.json")
DATA = os.path.join(BASE, "slowlog.jsonl")
ROTATE_BYTES = 10 * 1024 * 1024
KEEP_DAYS = 14
REDIS_CONTAINER = os.environ.get("REDIS_CONTAINER", "juhe-ai-redis")


def read_slowlog() -> list:
    # 密码来自容器内 REDIS_PASSWORD 环境变量，不在宿主机落明文
    out = subprocess.run(
        ["docker", "exec", REDIS_CONTAINER, "sh", "-c",
         'redis-cli --no-auth-warning -a "$REDIS_PASSWORD" --json SLOWLOG GET'],
        capture_output=True, text=True, timeout=30, check=True)
    return json.loads(out.stdout)


def main() -> int:
    os.makedirs(BASE, exist_ok=True)
    os.makedirs(PROM_DIR, exist_ok=True)
    last_id = -1
    if os.path.exists(STATE):
        with open(STATE, "r", encoding="utf-8") as f:
            last_id = json.load(f).get("last_id", -1)

    entries = sorted(read_slowlog(), key=lambda e: e[0])
    max_id = entries[-1][0] if entries else last_id
    if max_id < last_id:
        last_id = -1
    fresh = [e for e in entries if e[0] > last_id]

    for entry_id, ts, us, cmd_argv, client, cname in fresh:
        line = json.dumps({
            "id": entry_id, "ts": ts, "us": us,
            "cmd": cmd_argv[0] if cmd_argv else "?",
            "argv": cmd_argv[1:], "client": client, "cname": cname,
        }, ensure_ascii=False)
        with open(DATA, "a", encoding="utf-8") as f:
            f.write(line + "\n")

    if fresh and os.path.exists(DATA) and os.path.getsize(DATA) > ROTATE_BYTES:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        with open(DATA, "rb") as src, gzip.open(DATA + "." + stamp + ".gz", "wb") as dst:
            dst.write(src.read())
        open(DATA, "w").close()
    for old in glob.glob(DATA + ".*.gz"):
        if os.path.getmtime(old) < time.time() - KEEP_DAYS * 86400:
            os.remove(old)

    with open(STATE, "w", encoding="utf-8") as f:
        json.dump({"last_id": max(max_id, last_id), "updated_at": int(time.time())}, f)

    prom = os.path.join(PROM_DIR, "redis_slowlog.prom")
    tmp = prom + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write("# HELP juhe_redis_slowlog_new_entries New >10ms slow commands in latest snapshot run (5min cron)\n")
        f.write("# TYPE juhe_redis_slowlog_new_entries gauge\n")
        f.write("juhe_redis_slowlog_new_entries %d\n" % len(fresh))
        f.write("# HELP juhe_redis_slowlog_snapshot_timestamp_seconds Epoch of last successful snapshot\n")
        f.write("# TYPE juhe_redis_slowlog_snapshot_timestamp_seconds gauge\n")
        f.write("juhe_redis_slowlog_snapshot_timestamp_seconds %d\n" % int(time.time()))
    os.replace(tmp, prom)
    print("snapshot: fresh=%d last_id=%d" % (len(fresh), max(max_id, last_id)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
