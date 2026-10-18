# Homelab benchmark

Run `python3 scripts/benchmark.py` from a machine connected to the homelab before requesting review of every PR. Requires Python 3, Go, and Git. The script builds the current checkout once into a temporary directory, then times three sequential scans of `192.168.0.0/24` and `192.168.53.0/24` (512 addresses). The user has authorized these scans. This is a manual per-PR check; public GitHub-hosted runners cannot reach the homelab.

The default benchmark command is `find-nameservers -rate 500 -retry-gap 500ms -t 2.0 192.168.0.0/24 192.168.53.0/24`, using hostname `www.example.com`, three attempts per address, and batches of up to 1,024 addresses. `-t` is a final reply grace period after sending. The runner records the exact flags used. Earlier records retain the worker-based and unrestricted-burst scanner settings.

Use `python3 scripts/benchmark.py --rate 0 --retry-gap 50ms` to reproduce unrestricted sending, `--rate 500 --retry-gap 1s` to test wider retry spacing, and `--timeout 5` to test longer listening independently of pacing and retry settings. Each invocation runs three scans and appends a separate record; it always scans only the two fixed authorized prefixes. Build time is excluded. The first scan is included; no separate warm-up is performed. Each scan has a 120-second runner limit to fail visibly if it hangs.

Commit source changes before measuring, then commit the appended results separately. Each entry identifies the source commit tested, rather than the later commit containing the results. The script flags changes to Go sources, module files, or the runner. Include `BENCHMARK.md` in the PR and mention the median and any discovery changes in its description. Keep previous entries.

Compare median elapsed time and throughput (512 divided by elapsed seconds) under similar host and network conditions. Check the discovered server sets as well: missed servers must not look like a speed improvement. DNS responses with any response code count. If no servers are found, the entry is inconclusive, not a usable baseline. Investigate network access before comparing it. Wall time includes routing, ARP, server behavior, host load, and timeouts; this benchmark is not a CPU microbenchmark or a strict CI timing gate.

Execution failures leave no results entry and return a nonzero status. Network loss can instead appear as missing DNS servers because silence is normal for most scanned addresses. Preserve anomalous results and explain their cause when known.

## 2026-10-18T23:22:55+00:00

- Source revision: `4fe95618beed2f5f070129a9cd07a1bd754404a4`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, concurrency 32, timeout 2s; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 32.036 | 15.98 | 3 |
| 2 | 32.040 | 15.98 | 4 |
| 3 | 32.039 | 15.98 | 4 |

Median elapsed: **32.039s**; throughput at median: **15.98 addresses/s**.

Discovery sets: **differ across runs; compare coverage before timing.**

Run 1 discovered servers:

```text
192.168.0.1
192.168.53.2
192.168.53.53
```

Run 2 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

Run 3 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

## 2026-10-18T23:26:34+00:00

- Source revision: `0a3fdc0ef540fa9d47b3eb3c031b6ea43c51d792`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, concurrency 32, timeout 2s; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 32.039 | 15.98 | 4 |
| 2 | 32.046 | 15.98 | 4 |
| 3 | 32.048 | 15.98 | 4 |

Median elapsed: **32.046s**; throughput at median: **15.98 addresses/s**.

Discovery sets: identical across all runs.

Run 1 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

Run 2 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

Run 3 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

## 2026-10-18T23:46:16+00:00

- Source revision: `de81ff845dfb844899ea45941f596a673c818ae8`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, minimum retry gap 50ms, batch size 1024, reply grace 2s after final send; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 2.120 | 241.56 | 1 |
| 2 | 2.129 | 240.44 | 2 |
| 3 | 2.133 | 240.04 | 2 |

Median elapsed: **2.129s**; throughput at median: **240.44 addresses/s**.

Discovery sets: **differ across runs; compare coverage before timing.**

Run 1 discovered servers:

```text
192.168.0.1
```

Run 2 discovered servers:

```text
192.168.0.1
192.168.53.53
```

Run 3 discovered servers:

```text
192.168.0.1
192.168.53.53
```


### Same-session control (previous worker scanner)

One additional control scan of source `2cc291a2345d08c12d515d55dd8d9a6940af1b66` on the same machine, after the experimental measurements, took **32.033s** and found 4 servers. This is a single control measurement, not a three-run median. Build time was excluded; command and default worker settings match the historical baseline.

```text
192.168.0.1
192.168.53.1
192.168.53.2
192.168.53.53
```

## 2026-10-18T23:47:27+00:00

- Source revision: `de81ff845dfb844899ea45941f596a673c818ae8`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, minimum retry gap 50ms, batch size 1024, reply grace 2s after final send; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 2.120 | 241.56 | 3 |
| 2 | 2.119 | 241.67 | 3 |
| 3 | 2.115 | 242.13 | 3 |

Median elapsed: **2.119s**; throughput at median: **241.67 addresses/s**.

Discovery sets: identical across all runs.

Run 1 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.53
```

Run 2 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.53
```

Run 3 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.53
```

### Experimental comparison

The asynchronous experiment's first three-run median was 2.129s with only 1–2 discoveries. The previous worker scanner then found all four known servers in a same-session 32.033s control scan. A repeated asynchronous measurement had a 2.119s median (241.67 addresses/s), about 15.1 times faster than the prior 32.046s median, but found only three servers in every run, consistently missing `192.168.53.2`. Keep this as an experiment: elapsed time improved, but equivalent discovery coverage has not been achieved. The cause of missed replies is unconfirmed; changing network/neighbor state and burst loss remain possibilities.
