#!/usr/bin/env python3
"""Summarize the host CSV, load generator and plane probes for the soak report."""
from __future__ import annotations
import csv, json, pathlib, sys
from datetime import datetime

start = datetime.fromisoformat(sys.argv[1].replace('Z', '+00:00')) if len(sys.argv) > 1 else datetime.fromisoformat('2026-10-29T07:54:49+00:00')
end = datetime.fromisoformat(sys.argv[2].replace('Z', '+00:00')) if len(sys.argv) > 2 else datetime.fromisoformat('2026-10-29T08:24:49+00:00')
hold = start.timestamp() + 600
rows = []
with open('artifacts/soak/host.csv', newline='') as f:
    for row in csv.DictReader(f):
        ts = datetime.fromisoformat(row['timestamp'].replace('Z', '+00:00'))
        if start <= ts <= end:
            row['_ts'] = ts.timestamp()
            rows.append(row)

def nums(key, subset=None):
    out = []
    for row in (subset if subset is not None else rows):
        try:
            out.append(float(row[key]))
        except (KeyError, ValueError):
            pass
    return out

def stat(key, subset=None):
    values = nums(key, subset)
    if not values:
        return {}
    return {'min': min(values), 'max': max(values), 'avg': round(sum(values)/len(values), 2)}

hold_rows = [row for row in rows if row['_ts'] >= hold]
summary = {
    'window': {'start': start.isoformat(), 'end': end.isoformat(), 'samples': len(rows), 'holdSamples': len(hold_rows)},
    'all': {key: stat(key) for key in ['load1','load5','load15','mem_avail_mb','mem_used_mb','imap_conns','imap_procs','hibernate_rss_kb','db_conns','go_rss_kb','admin_rss_kb','control_rss_kb','disk_used_pct']},
    'hold': {key: stat(key, hold_rows) for key in ['load1','load5','load15','mem_avail_mb','mem_used_mb','imap_conns','imap_procs','hibernate_rss_kb','db_conns','go_rss_kb','admin_rss_kb','control_rss_kb','disk_used_pct']},
    'health': {
        'web_non200': sum(1 for row in rows if row.get('web_health') not in ('200', '')),
        'control_non200': sum(1 for row in rows if row.get('control_health') not in ('200', '')),
        'services_min': min((int(row['services_active']) for row in rows if row.get('services_active')), default=0),
    },
}
for path in ['artifacts/soak/run/soak-summary.json', 'artifacts/soak/plane-probe.json']:
    p = pathlib.Path(path)
    if p.exists():
        try:
            summary[path] = json.loads(p.read_text())
        except Exception as exc:
            summary[path] = {'error': str(exc)}
pathlib.Path('artifacts/soak/analysis.json').write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
