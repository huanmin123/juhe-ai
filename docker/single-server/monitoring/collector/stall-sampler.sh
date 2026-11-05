#!/bin/sh
# 僵死窗口取证采样器：每分钟采集主机 load/PSI + PG 活动语句（含阻塞关系）。
#
# 背景：磁盘 I/O 风暴导致整机僵死时，持锁事务会被饿死在事务中间，事后难以定罪到
# 具体语句，因此需要窗口期实时采样。本脚本只读 /proc 与 pg_stat_activity，本地执行
# 零网络拉取，每分钟一次符合监控频率预算。
#
# 安装：复制到 /opt/juhe-monitoring/stall-sampler.sh，并在 /etc/cron.d/juhe-stall-sampler
# 写一行（见同目录 cron/juhe-stall-sampler）。日志与 PSI 指标同步产出。
#
# 数据：/opt/juhe-monitoring/stall-sampler/{host.log,pg-activity.log}（超 40MB 截尾滚动）
# 排障：看窗口时段 pg-activity.log 中 q_s 最大且 blk_by 为空的行 = 嫌疑持有者/长语句。
set -u
DIR=/opt/juhe-monitoring/stall-sampler
PG_CONTAINER="${PG_CONTAINER:-juhe-ai-postgres}"
PG_USER="${PG_USER:-juhe_ai}"
PG_DB="${PG_DB:-juhe_ai}"
mkdir -p "$DIR"
TS=$(date '+%F %T')

LOAD=$(cut -d' ' -f1-3 /proc/loadavg | tr ' ' '/')
CPU60=$(awk '/^some/{for(i=1;i<=NF;i++)if($i~/^avg60=/)print $i}' /proc/pressure/cpu)
IOS=$(awk '/^some/{for(i=1;i<=NF;i++)if($i~/^avg60=/)print $i}' /proc/pressure/io)
IOF=$(awk '/^full/{for(i=1;i<=NF;i++)if($i~/^avg60=/)print $i}' /proc/pressure/io)
MEMAVAIL=$(awk '/^MemAvailable:/{print int($2/1024)}' /proc/meminfo)
SWAPFREE=$(awk '/^SwapFree:/{print int($2/1024)}' /proc/meminfo)
SWAPUSED=$(( $(awk '/^SwapTotal:/{print int($2/1024)}' /proc/meminfo) - SWAPFREE ))
PSIM=$(awk '/^some/{for(i=1;i<=NF;i++)if($i~/^avg60=/)print $i}' /proc/pressure/memory)
echo "$TS load=$LOAD cpu_$CPU60 io_some_$IOS io_full_$IOF mem_avail_mb=$MEMAVAIL swap_used_mb=$SWAPUSED psi_mem_$PSIM" >> "$DIR/host.log"

# textfile 指标（node-exporter --collector.textfile.directory）：主机 PSI 告警数据源。
# tmp+mv 原子写防 node-exporter 抓到半截文件；单指标值缺失时跳过该行，
# PSI 整体不可用时输出空文件使序列消失而非滞留旧值。
IOS_VAL=${IOS#avg60=}
IOF_VAL=${IOF#avg60=}
CPU_VAL=${CPU60#avg60=}
PSIM_VAL=${PSIM#avg60=}
mkdir -p /opt/juhe-monitoring/textfile
PSI_TXT=/opt/juhe-monitoring/textfile/host_psi.prom
{
  [ -n "$IOF_VAL" ] && {
    echo "# HELP juhe_host_psi_io_full_avg60 PSI IO full avg60 percent (stall-sampler)."
    echo "# TYPE juhe_host_psi_io_full_avg60 gauge"
    echo "juhe_host_psi_io_full_avg60 $IOF_VAL"
  }
  [ -n "$IOS_VAL" ] && {
    echo "# HELP juhe_host_psi_io_some_avg60 PSI IO some avg60 percent (stall-sampler)."
    echo "# TYPE juhe_host_psi_io_some_avg60 gauge"
    echo "juhe_host_psi_io_some_avg60 $IOS_VAL"
  }
  [ -n "$CPU_VAL" ] && {
    echo "# HELP juhe_host_psi_cpu_some_avg60 PSI CPU some avg60 percent (stall-sampler)."
    echo "# TYPE juhe_host_psi_cpu_some_avg60 gauge"
    echo "juhe_host_psi_cpu_some_avg60 $CPU_VAL"
  }
  [ -n "$PSIM_VAL" ] && {
    echo "# HELP juhe_host_psi_mem_some_avg60 PSI memory some avg60 percent (stall-sampler)."
    echo "# TYPE juhe_host_psi_mem_some_avg60 gauge"
    echo "juhe_host_psi_mem_some_avg60 $PSIM_VAL"
  }
  true
} > "$PSI_TXT.tmp" && mv "$PSI_TXT.tmp" "$PSI_TXT"

docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -At -c "
SELECT to_char(now(),'YYYY-MM-DD HH24:MI:SS')||'|pid='||pid
      ||'|app='||coalesce(application_name,'-')
      ||'|wait='||coalesce(wait_event_type,'-')||'/'||coalesce(wait_event,'-')
      ||'|xact_s='||coalesce(round(extract(epoch from (now()-xact_start))::numeric,0),0)
      ||'|q_s='||round(extract(epoch from (now()-query_start))::numeric,0)
      ||'|blk_by=['||array_to_string(pg_blocking_pids(pid),',')||']'
      ||'|q='||left(regexp_replace(query,'[[:space:]]+',' ','g'),200)
FROM pg_stat_activity
WHERE backend_type='client backend' AND state<>'idle' AND now()-query_start>interval '2 seconds'
ORDER BY query_start;" >> "$DIR/pg-activity.log" 2>> "$DIR/pg-activity.err"

for f in "$DIR/host.log" "$DIR/pg-activity.log"; do
  [ -f "$f" ] || continue
  if [ "$(stat -c%s "$f")" -gt 41943040 ]; then
    tail -n 5000 "$f" > "$f.trunc" && mv "$f.trunc" "$f"
  fi
done
