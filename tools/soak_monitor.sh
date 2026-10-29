#!/bin/bash
set -u
OUT="${1:-/var/log/soak-host.csv}"
STOP=/tmp/soak-monitor.stop
rm -f "$STOP"
printf '%s\n' 'timestamp,load1,load5,load15,mem_total_mb,mem_avail_mb,mem_used_mb,imap_conns,imap_procs,imap_rss_kb,hibernate_procs,hibernate_rss_kb,db_conns,go_procs,go_rss_kb,admin_rss_kb,control_rss_kb,disk_used_pct,web_health,control_health,services_active' > "$OUT"
while [ ! -e "$STOP" ]; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  read -r load1 load5 load15 < <(awk '{print $1, $2, $3}' /proc/loadavg)
  read -r mem_total mem_avail mem_used < <(free -m | awk '/^Mem:/{print $2, $7, $3}')
  imap_conns=$(ss -tn state established '( sport = :993 )' 2>/dev/null | tail -n +2 | wc -l)
  read -r imap_procs imap_rss < <(ps -eo rss,comm | awk '$2 ~ /^imap$/ {n++; sum+=$1} END {print n+0, sum+0}')
  read -r hib_procs hib_rss < <(ps -eo rss,comm | awk '$2 ~ /imap-hibernate/ {n++; sum+=$1} END {print n+0, sum+0}')
  db_conns=$(runuser -u postgres -- psql -d meridian -Atc 'select count(*) from pg_stat_activity' 2>/dev/null || echo 0)
  read -r go_procs go_rss < <(ps -eo rss,comm | awk '$2 ~ /^meridian-server$/ {n++; sum+=$1} END {print n+0, sum+0}')
  admin_rss=$(ps -eo rss,comm | awk '$2 ~ /^meridian-admin/ {sum+=$1} END {print sum+0}')
  control_rss=$(ps -eo rss,comm | awk '$2 ~ /^alt8mail-contro/ {sum+=$1} END {print sum+0}')
  disk_used=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
  web_health=$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' https://127.0.0.1/api/health 2>/dev/null || echo 000)
  control_health=$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' https://127.0.0.1:8808/healthz 2>/dev/null || echo 000)
  services_active=$(systemctl is-active postfix dovecot rspamd clamav-daemon nginx postgresql@15-main redis-server alt8mail-control meridian-server meridian-admin 2>/dev/null | grep -c '^active$')
  printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
    "$ts" "$load1" "$load5" "$load15" "$mem_total" "$mem_avail" "$mem_used" \
    "$imap_conns" "$imap_procs" "$imap_rss" "$hib_procs" "$hib_rss" "$db_conns" \
    "$go_procs" "$go_rss" "$admin_rss" "$control_rss" "$disk_used" "$web_health" "$control_health" "$services_active" >> "$OUT"
  sleep 5
done
