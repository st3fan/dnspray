# Homelab benchmark

Run `python3 scripts/benchmark.py` from a machine connected to the homelab before requesting review of every PR. Requires Python 3, Go, and Git. The script builds the current checkout once into a temporary directory, then times three sequential scans of `192.168.0.0/24` and `192.168.53.0/24` (512 addresses). The user has authorized these scans. This is a manual per-PR check; public GitHub-hosted runners cannot reach the homelab.

The fixed command is `find-nameservers 192.168.0.0/24 192.168.53.0/24`, using the default hostname (`www.example.com`), concurrency (32), and query timeout (2 seconds). Build time is excluded. The first scan is included; no separate warm-up is performed. Each scan has a 120-second runner limit to fail visibly if it hangs.

Commit source changes before measuring, then commit the appended results separately. Each entry identifies the source commit tested, rather than the later commit containing the results. The script flags changes to Go sources, module files, or the runner. Include `BENCHMARK.md` in the PR and mention the median and any discovery changes in its description. Keep previous entries.

Compare median elapsed time and throughput (512 divided by elapsed seconds) under similar host and network conditions. Check the discovered server sets as well: missed servers must not look like a speed improvement. DNS responses with any response code count. If no servers are found, the entry is inconclusive, not a usable baseline. Investigate network access before comparing it. Wall time includes routing, ARP, server behavior, host load, and timeouts; this benchmark is not a CPU microbenchmark or a strict CI timing gate.

Execution failures leave no results entry and return a nonzero status. Network loss can instead appear as missing DNS servers because silence is normal for most scanned addresses. Preserve anomalous results and explain their cause when known.
