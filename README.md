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

Capability checks are opt-in. Without `-test-*` options, print discovered addresses only. When tests are selected, discovery finishes first and then each discovered server is inspected with only the selected tests. Inspection uses at most 8 concurrent server workers (`-check-workers`) and a 2s deadline per check (`-check-timeout`). Check failures are reported individually and never remove a discovered address. Detailed inspection can take longer than the roughly 2s discovery phase.

`-test-version` queries `version.bind. TXT CH` for self-reported version strings. Repeat `-test-recursion name` to compare non-recursive and recursive A queries for each supplied external name; no implicit recursion test is performed. Use names outside the target's authoritative zones; a controlled, previously uncached name gives stronger evidence. RA flags, authoritative answers, and cached answers alone do not establish public recursion. Reports describe behavior from this scanner's address, not access from every client.

```sh
find-nameservers -json -test-version -test-recursion probe.your-domain.example 192.0.2.0/24
find-nameservers -discover-only 192.0.2.0/24
```

`-json` outputs a JSON array with an address and independent check results, including statuses, evidence, and returned version strings. Selected tests produce human-readable reports by default. With no tests, `-json` produces address records with empty check lists. `-discover-only` emits one address per line and skips every capability check; it cannot be combined with `-json` or `-test-*` options. Benchmarks explicitly select this discovery-only mode. Timeouts and inconclusive answers are reported separately from refusals.

### Authority

Repeat `-test-authority domain` to ask each discovered server a non-recursive SOA question for each domain. A validated AA=1 answer with NOERROR or NXDOMAIN is reported as `observed` authority for that query. AA=0 is `not-authoritative`; cached SOAs and referrals do not establish authority. Refusals, timeouts, and errors keep separate outcomes. JSON retains AA/RCODE observations and SOA records from answer/authority sections.

An authoritative negative response can contain a parent zone's SOA. This establishes an authoritative answer about the queried name; it does not establish that the name is itself a zone apex.

```sh
find-nameservers -test-authority example.com -test-authority example.net -test-version 192.0.2.0/24
```

### Zone transfers

Specify `-test-axfr` once for each zone apex to test. Every discovered server is tested against every supplied zone, with a separate status for each pair:

```sh
find-nameservers -test-axfr example.com -test-axfr example.net 192.0.2.0/24
```

No AXFR check is scheduled unless `-test-axfr` is supplied. Zones are not guessed or enumerated: a refusal for one zone says nothing about transfer permissions for other zones.

AXFR uses TCP and shares the `-check-timeout` per-check deadline. A transfer counts as `observed` only after matching opening and closing SOA records. Transfers are streamed and counted; zone contents are not retained. Each transfer is limited to 10,000 records (`-axfr-max-records`, including both SOAs) and 1MiB of DNS TCP data (`-axfr-max-bytes`, including length framing). Limits produce `limited`, interrupted transfers produce `partial`, and no-response deadlines produce `timeout`, rather than claiming AXFR is unavailable. JSON includes record and byte counts. A successful transfer establishes permission from this scanner for that zone at that time.
