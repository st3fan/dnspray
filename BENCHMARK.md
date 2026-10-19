# Homelab benchmark

Run `python3 scripts/benchmark.py` from a machine connected to the homelab before requesting review of every PR. Requires Python 3, Go, and Git. The script builds the current checkout once into a temporary directory, then times three sequential scans of `192.168.0.0/24` and `192.168.53.0/24` (512 addresses). The user has authorized these scans. This is a manual per-PR check; public GitHub-hosted runners cannot reach the homelab.

The default benchmark command is `find-nameservers -rate 100 -retry-gap 1s -t 2.0 192.168.0.0/24 192.168.53.0/24`, using hostname `www.example.com`, three attempts per address, and batches of up to 1,024 addresses. `-t` is a final reply grace period after sending. The runner records the exact flags used. Earlier records retain the worker-based and unrestricted-burst scanner settings.

Use `python3 scripts/benchmark.py --rate 0 --retry-gap 50ms` to reproduce unrestricted sending, `--rate 500 --retry-gap 500ms` or `--rate 500 --retry-gap 1s` to compare faster sending and retry spacing, and `--timeout 5` to test longer listening independently of pacing and retry settings. Each invocation runs three scans and appends a separate record; it always scans only the two fixed authorized prefixes. Build time is excluded. The first scan is included; no separate warm-up is performed. Each scan has a 120-second runner limit to fail visibly if it hangs.

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

## 2026-10-18T23:53:45+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 0 -retry-gap 50ms -t 2.0 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 2.119 | 241.63 | 4 |
| 2 | 2.129 | 240.46 | 4 |
| 3 | 2.124 | 241.00 | 4 |

Median elapsed: **2.124s**; throughput at median: **241.00 addresses/s**.

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

## 2026-10-18T23:54:01+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 0 -retry-gap 50ms -t 5.0 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 5.121 | 99.98 | 3 |
| 2 | 5.125 | 99.91 | 2 |
| 3 | 5.122 | 99.96 | 3 |

Median elapsed: **5.122s**; throughput at median: **99.96 addresses/s**.

Discovery sets: **differ across runs; compare coverage before timing.**

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
```

Run 3 discovered servers:

```text
192.168.0.1
192.168.53.1
192.168.53.53
```

## 2026-10-18T23:54:19+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 500 -retry-gap 500ms -t 2 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 5.880 | 87.08 | 3 |
| 2 | 5.864 | 87.31 | 3 |
| 3 | 5.884 | 87.01 | 3 |

Median elapsed: **5.880s**; throughput at median: **87.08 addresses/s**.

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

## 2026-10-18T23:54:36+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 500 -retry-gap 1s -t 2 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 5.838 | 87.71 | 3 |
| 2 | 5.863 | 87.32 | 3 |
| 3 | 5.859 | 87.39 | 3 |

Median elapsed: **5.859s**; throughput at median: **87.39 addresses/s**.

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

## 2026-10-18T23:55:03+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 500 -retry-gap 500ms -t 5.0 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 8.861 | 57.78 | 3 |
| 2 | 8.880 | 57.66 | 4 |
| 3 | 8.860 | 57.79 | 4 |

Median elapsed: **8.861s**; throughput at median: **57.78 addresses/s**.

Discovery sets: **differ across runs; compare coverage before timing.**

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

## 2026-10-18T23:56:04+00:00

- Source revision: `06ef2b4d1f460247fbaf0542f2b7569a90031732`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 100 -retry-gap 1s -t 2.0 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 18.350 | 27.90 | 4 |
| 2 | 18.333 | 27.93 | 4 |
| 3 | 18.351 | 27.90 | 4 |

Median elapsed: **18.350s**; throughput at median: **27.90 addresses/s**.

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

## 2026-10-18T23:57:19+00:00

- Source revision: `b8512b5351aa7f905628650bbc5a7e5c813e6331`
- Source/runner state: clean
- Go: `go version go1.27.1 linux/amd64`
- OS/architecture: `Linux-7.0.0-34-generic-x86_64-with-glibc2.43` / `x86_64`
- CPU: Intel(R) Core(TM) i5-8400 CPU @ 2.80GHz (6 logical CPUs)
- Command: `find-nameservers -rate 100 -retry-gap 1s -t 2 192.168.0.0/24 192.168.53.0/24`
- Settings: hostname `www.example.com`, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; 512 addresses; three sequential runs; build time excluded.

| Run | Elapsed (s) | Addresses/s | DNS servers |
| --- | ---: | ---: | ---: |
| 1 | 18.345 | 27.91 | 4 |
| 2 | 18.326 | 27.94 | 4 |
| 3 | 18.352 | 27.90 | 4 |

Median elapsed: **18.345s**; throughput at median: **27.91 addresses/s**.

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


### Reliability follow-up

Added global send pacing and configurable retry spacing. The final defaults are 100 packets/second, a minimum one-second retry gap, and two seconds of listening after the final send. Attempts remain fixed at three. All comparisons below scanned only the two authorized prefixes.

| Packets/s | Retry gap | Final grace | Median elapsed | Discoveries per run |
| --- | --- | --- | ---: | --- |
| 0 | 50ms | 2.0s | 2.124s | 4, 4, 4 |
| 0 | 50ms | 5.0s | 5.122s | 3, 2, 3 |
| 500 | 500ms | 2s | 5.880s | 3, 3, 3 |
| 500 | 1s | 2s | 5.859s | 3, 3, 3 |
| 500 | 500ms | 5.0s | 8.861s | 3, 4, 4 |
| 100 | 1s | 2.0s | 18.350s | 4, 4, 4 |
| 100 (final defaults) | 1s | 2s | 18.345s | 4, 4, 4 |

The 100pps/1s/2s profile found the same four known servers in all six measured runs, including three on final source revision `b8512b5`. Its final median was 18.345s, about 1.75 times faster than the prior 32.046s worker median, with all four servers found. Faster profiles did not maintain coverage across the full comparison. At 500pps, a full pass over 512 targets itself exceeds one second, so configured 500ms and 1s minimum retry gaps do not force materially different actual retry spacing.

Diagnostics: a scoped tcpdump capture was attempted, but this environment lacks CAP_NET_RAW and capture failed before any traffic was collected. A read-only neighbor-table snapshot during the 500pps comparison showed `192.168.53.2` as INCOMPLETE while `192.168.53.1` and `192.168.53.53` were REACHABLE. This is evidence of unresolved neighbor state at that moment, not proof of the cause of every missed reply. No neighbor caches were flushed or host-wide network settings changed. Runs were sequential rather than randomized, and even unrestricted sending found four servers in one set before losing coverage in another. These observations support using the measured lower-rate profile but do not establish a controlled causal explanation or guarantee discovery on every future run.


### Packet-capture findings and host limitation (2026-10-19)

Once tcpdump was available, a reproduced miss showed three queries outbound at approximately 5ms, 59ms, and 110ms after startup but no matching reply in the capture. Later successful replies took roughly 1–1.5ms. Three further unrestricted scans captured DNS across both authorized prefixes; every known server with a captured matching reply was reported. Both capture sessions reported zero kernel capture-buffer drops, and the UDP receive-buffer error counter was zero. These are diagnostic observations, not additional standardized benchmark sets. Raw traffic captures remain outside the repository for separate host-to-host transfer.

The user clarified that the two individual DNS server addresses are OpenBSD/Unbound servers and the third is their shared CARP address; all are expected to respond. The user also reports that this host's router intercepts external DNS. External scans from this host would therefore be misleading. Move routed testing to a host with direct, non-intercepted internet access and a user-selected remote test scope. Retain these local results with their topology and environment caveats; they do not establish optimal remote-scanning defaults.
