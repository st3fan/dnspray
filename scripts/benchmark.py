#!/usr/bin/env python3
"""Build once, measure three homelab scans, and append a Markdown record."""

import argparse
import datetime
import ipaddress
import os
from pathlib import Path
import platform
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent.parent
PREFIXES = ("192.168.0.0/24", "192.168.53.0/24")
ADDRESSES = sum(ipaddress.ip_network(prefix).num_addresses for prefix in PREFIXES)
RUNS = 3


def output(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def cpu_model():
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return platform.processor() or "unknown"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rate", type=int, default=0, help="packets/second; 0 disables pacing")
    parser.add_argument("--retry-gap", default="50ms", help="Go duration, e.g. 500ms or 1s")
    parser.add_argument("--timeout", type=float, default=2, help="seconds to listen after final send")
    args = parser.parse_args()
    flags = ("-discover-only", "-rate", str(args.rate), "-retry-gap", args.retry_gap, "-t", str(args.timeout))
    command = ("dnspray", *flags, *PREFIXES)
    revision = output("git", "rev-parse", "HEAD")
    # The results file changes with every run; it does not affect the scanner.
    dirty = output("git", "status", "--porcelain", "--untracked-files=normal",
                   "--", "*.go", "go.mod", "go.sum", "scripts/")
    environment = [
        f"- Source revision: `{revision}`",
        f"- Source/runner state: {'dirty' if dirty else 'clean'}",
        f"- Go: `{output('go', 'version')}`",
        f"- OS/architecture: `{platform.platform()}` / `{platform.machine()}`",
        f"- CPU: {cpu_model()} ({os.cpu_count()} logical CPUs)",
        f"- Command: `{' '.join(command)}`",
        "- Settings: discovery query `. NS IN` with RD=0, 3 attempts per address, batch size 1024; send rate, minimum retry gap, and final reply grace as specified in the command; "
        "512 addresses; three sequential runs; build time excluded.",
    ]
    if dirty:
        environment.append("- Changed source/runner paths:\n\n```text\n" + dirty + "\n```")

    results = []
    with tempfile.TemporaryDirectory(prefix="dnspray-benchmark-") as temp:
        binary = str(Path(temp) / "dnspray")
        print("Building scanner (excluded from timing)...", flush=True)
        subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT, check=True)
        for index in range(1, RUNS + 1):
            print(f"Run {index}/{RUNS}: {' '.join(command)}", flush=True)
            start = time.perf_counter()
            run = subprocess.run([binary, *flags, *PREFIXES], cwd=ROOT, text=True,
                                 capture_output=True, check=True, timeout=120)
            elapsed = time.perf_counter() - start
            servers = set()
            for line in run.stdout.splitlines():
                addr = ipaddress.ip_address(line.strip())
                if not any(addr in ipaddress.ip_network(p) for p in PREFIXES):
                    raise ValueError(f"Unexpected result outside benchmark prefixes: {addr}")
                servers.add(str(addr))
            if run.stderr:
                print(run.stderr, file=sys.stderr, end="")
            results.append((elapsed, servers))
            print(f"  {elapsed:.3f}s, {ADDRESSES / elapsed:.2f} addresses/s, "
                  f"{len(servers)} DNS servers", flush=True)

    median = statistics.median(elapsed for elapsed, _ in results)
    lines = ["", "## " + datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
             "", *environment, "", "| Run | Elapsed (s) | Addresses/s | DNS servers |",
             "| --- | ---: | ---: | ---: |"]
    for index, (elapsed, servers) in enumerate(results, 1):
        lines.append(f"| {index} | {elapsed:.3f} | {ADDRESSES / elapsed:.2f} | {len(servers)} |")
    lines += ["", f"Median elapsed: **{median:.3f}s**; throughput at median: "
              f"**{ADDRESSES / median:.2f} addresses/s**.", ""]
    same = all(servers == results[0][1] for _, servers in results)
    lines.append("Discovery sets: " + ("identical across all runs." if same else
                                      "**differ across runs; compare coverage before timing.**"))
    if not any(servers for _, servers in results):
        warning = "No DNS servers discovered: connectivity is unconfirmed; this is not a usable performance baseline."
        lines += ["", "**" + warning + "**"]
        print(warning, file=sys.stderr)
    for index, (_, servers) in enumerate(results, 1):
        lines += ["", f"Run {index} discovered servers:", "", "```text",
                  *sorted(servers, key=ipaddress.ip_address), "```"]
    lines.append("")
    # Append only after all runs succeed; failures must not leave partial records.
    with (ROOT / "BENCHMARK.md").open("a") as record:
        record.write("\n".join(lines))
    print("Appended results to BENCHMARK.md.", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr, end="")
        sys.exit(f"Benchmark failed; no results appended: {error}")
