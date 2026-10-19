# find-nameservers

Scan every address in one or more IPv4 or IPv6 CIDRs for a DNS server on UDP port 53. Individual IP addresses are also accepted. Each responding address is collected for capability inspection. Use `-discover-only` for one address per line.

## Install

With Go 1.27.1 or newer:

```sh
go install github.com/st3fan/find-nameservers@latest
```

The `find-nameservers` executable is installed in `GOBIN`, or in `$(go env GOPATH)/bin` if `GOBIN` is unset. Add that directory to your `PATH` to run it by name.

## Usage

```sh
find-nameservers -h example.org -t 2 192.0.2.0/24 2001:db8::1
```

Discovery defaults to a non-recursive root NS query (`. NS IN`, RD=0). `-h` overrides it with a non-recursive A query. The scanner sends three UDP queries per address with at least 50ms between attempts by default, using batches of up to 1,024 addresses. Replies are collected concurrently on one shared socket per address family. Each responding address is recorded once. Any valid matching DNS response counts, including `NXDOMAIN` or `REFUSED`.

The defaults are tuned for a gigabit connection: sending is unpaced (`-rate 0`), with a 50ms minimum retry gap and a 2s final listening period. For slower or lossy networks, try `-rate 100 -retry-gap 1s` and compare discovered addresses as well as runtime.

`-rate` sets a global maximum send rate, shared across targets, retries, and both address families. The default `-rate 0` disables pacing. `-retry-gap` sets the positive minimum delay between attempts to the same address (default `50ms`; accepts Go durations such as `1s`). Actual delays may be longer because of pacing and batch scheduling. Delayed sends do not accumulate credit for a catch-up burst.

`-t` is a positive number of seconds (default 2) to keep listening **after the final request is sent**. Listening also runs throughout sending. Total runtime includes sending, retry delays, and this final grace period. Ctrl+C stops sending and listening. The former `-c` worker-concurrency flag has been removed for this experiment.

Sending and retry storage are bounded; remembered discoveries grow with the number of responding addresses. Overlapping prefixes may cause additional probes, but duplicate replies produce only one output line. Send, receive, and output errors fail the scan visibly. High-speed UDP probing can lose packets, so compare discovered servers as well as elapsed time.

The implementation uses [`github.com/miekg/dns`](https://github.com/miekg/dns) to construct queries, exchange DNS messages over UDP, and parse responses.

`-discover-only` explicitly selects discovery without capability inspection and preserves one-IP-per-line output for scripts and benchmarks. Discovered addresses are also collected once in a typed list for subsequent inspection.

## Server inspection

By default, discovery finishes first and then each discovered server is inspected. Inspection uses at most 8 concurrent server workers (`-check-workers`) and a 2s deadline per check (`-check-timeout`). Check failures are reported individually and never remove a discovered address. Detailed inspection can take longer than the roughly 2s discovery phase.

The initial checks query `version.bind. TXT CH` for self-reported version strings and compare non-recursive and recursive A queries for `www.example.com.`. Supply `-recursion-name name` multiple times to test your own external names. Use names outside the target's authoritative zones; a controlled, previously uncached name gives stronger evidence. RA flags, authoritative answers, and cached answers alone do not establish public recursion. Reports describe behavior from this scanner's address, not access from every client.

```sh
find-nameservers -json -recursion-name probe.your-domain.example 192.0.2.0/24
find-nameservers -discover-only 192.0.2.0/24
```

`-json` outputs a JSON array with an address and independent check results, including statuses, evidence, and returned version strings. Human-readable reports are the default. `-discover-only` emits one address per line and skips every capability check; it cannot be combined with `-json`. Benchmarks explicitly select this discovery-only mode. Timeouts and inconclusive answers are reported separately from refusals.
