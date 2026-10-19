# Project

This is a reconnaissance tool for finding DNS servers within a supplied list of network prefixes.

The tool is written in Go.

# Method of working

1. Plan the work in a GitHub issue. Describe the proposed changes, scope, and validation, and have the user review the issue before implementation.
2. Implement the reviewed plan in a pull request for simple work.
3. For complicated work, use a stack of pull requests. Split the work into focused, reviewable layers, with foundational changes below the changes that depend on them.
4. Link the pull request or stack to the planning issue and explain how the changes were validated.
5. When the user asks to merge a pull request, treat the work as complete. After merging, switch back to the repository's default branch and update it from the remote so the next task starts there. Preserve any unrelated local changes.

Use the repository's [gh-stack skill](.agents/skills/gh-stack/SKILL.md) to manage stacked pull requests. Read and follow the skill before creating or modifying a stack, and create the stack before implementing its layers.

# Performance benchmarks

Run `python3 scripts/benchmark.py` from the homelab before requesting review of every pull request and commit its appended results in `BENCHMARK.md`, including the tested revision, environment, command, elapsed time, and discovered DNS servers so results can be compared across changes.

The user authorizes running `find-nameservers 192.168.0.0/24 192.168.53.0/24` for this benchmark. Development takes place in the `192.168.0.0/16` homelab; the benchmark scans the two specified `/24` prefixes.

Commit source changes before measuring, then commit the benchmark results separately. Keep historical entries. Include the median runtime and any discovery differences in the PR description. If homelab access is unavailable, document that limitation in `BENCHMARK.md` and the PR; do not report failed connectivity as a performance baseline.

# Host migration handoff (2026-10-19)

Current work is on `async-dns-scan`, tracked by [issue #6](https://github.com/st3fan/find-nameservers/issues/6) and [PR #7](https://github.com/st3fan/find-nameservers/pull/7). The PR remains open; committing or moving hosts does not mean merging it.

The current host can test only the local homelab reliably. The user reports that its router intercepts outbound DNS (DNS captive), so external DNS scans from this host would be misleading. Continue routed/remote experiments from a different host with a direct, non-intercepted internet connection. Obtain the intended remote test prefixes from the user before running them; no remote target has been selected yet. The existing benchmark runner fixes the two authorized homelab prefixes and needs a reviewed change before it can serve as a routed benchmark. If the new host cannot reach the homelab, record that limitation rather than treating empty results as a benchmark.

The homelab DNS addresses `192.168.53.1` and `192.168.53.2` are two OpenBSD servers running Unbound; `192.168.53.53` is their shared CARP address. All three are expected to answer, but they represent two machines rather than three independent servers. Local ARP and this topology may not represent routed scans.

Current implementation: shared UDP sockets per address family; separate receivers; three attempts per address; batches of up to 1,024 targets; `-rate 100` packets/second globally, `-retry-gap 1s`, and `-t 2` seconds of listening after the final send. `-rate 0` disables pacing; `-c` was retired. miekg/dns handles DNS messages. Replies are validated against sent targets, address-scoped IDs, port, opcode, flags, and question, then deduplicated.

See `BENCHMARK.md` for measurements and diagnostics. Previous worker median: 32.046s. Unrestricted bursts: about 2.12s with variable discovery. Final 100pps profile: 18.345s median and all four known servers in six measured runs. These results do not establish the best remote-scanning defaults. Tests, race detection, vet, and whitespace checks passed on the implementation.

A subsequent capture reproduced a miss: three queries to the missing server appeared outbound but no matching reply appeared in the capture. Later successful replies took roughly 1–1.5ms. In three additional full-prefix captures, every known server with a captured matching reply was reported. Capture-buffer drops and UDP receive-buffer errors were zero. The precise network/server-side loss mechanism remains unconfirmed; server-side capture would narrow it down. Raw captures stay outside Git and were archived locally on the old host at `/tmp/find-nameservers-captures-2026-10-19.tar.gz`; arrange a separate host-to-host transfer if needed.

On the new host, read this file, `BENCHMARK.md`, and PR #7 before continuing. Build with `go build .`; validate with `go test ./...`, `go test -race ./...`, and `go vet ./...`. Next work is to establish a representative authorized routed benchmark and revisit rate/retry defaults there, rather than continuing to tune solely for CARP on the local LAN.
