#!/usr/bin/env python3
"""Quantiles of a Prometheus histogram scraped from a /metrics endpoint.

  python3 loadtest/histogram.py http://timeline:9103/metrics waypoint_sync_latency_seconds
reads the text format from stdin when the URL is "-".
"""
import re
import sys
import urllib.request
from collections import defaultdict

url, name = sys.argv[1], sys.argv[2]
text = sys.stdin.read() if url == "-" else urllib.request.urlopen(url, timeout=5).read().decode()
buckets = defaultdict(dict)  # labels -> {le: count}
for line in text.splitlines():
    m = re.match(rf'{name}_bucket\{{(.*)\}} (\S+)', line)
    if not m:
        continue
    labels = dict(re.findall(r'(\w+)="([^"]*)"', m.group(1)))
    le = labels.pop("le")
    key = ",".join(f"{k}={v}" for k, v in sorted(labels.items()))
    buckets[key][float("inf") if le == "+Inf" else float(le)] = float(m.group(2))

def quantile(b, q):
    edges = sorted(b)
    total = b[edges[-1]]
    if total == 0:
        return float("nan")
    rank, prev_edge, prev_count = q * total, 0.0, 0.0
    for e in edges:
        if b[e] >= rank:
            if e == float("inf"):
                return prev_edge
            return prev_edge + (e - prev_edge) * (rank - prev_count) / max(b[e] - prev_count, 1e-9)
        prev_edge, prev_count = e, b[e]
    return edges[-1]

for key, b in buckets.items():
    n = b[float("inf")]
    qs = " ".join(f"p{int(q*100)}={quantile(b, q)*1000:.1f}ms" for q in (0.5, 0.9, 0.95, 0.99))
    print(f"{key or name}: n={n:.0f} {qs}")
