# find-nameservers

Scan every address in IPv4 or IPv6 CIDRs, or in an ASN’s originated IPv4 prefixes, for a DNS server on UDP port 53. Individual IP addresses are also accepted. Each responding address is collected for capability inspection. Use `-discover-only` for one address per line.

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

Sending and retry storage are bounded; remembered discoveries grow with the number of responding addresses. Duplicate and contained target prefixes are removed before scanning, and duplicate replies produce only one output line. Send, receive, and output errors fail the scan visibly. High-speed UDP probing can lose packets, so compare discovered servers as well as elapsed time.

The implementation uses [`github.com/miekg/dns`](https://github.com/miekg/dns) to construct queries, exchange DNS messages over UDP, and parse responses.

`-discover-only` explicitly selects discovery without capability inspection and preserves one-IP-per-line output for scripts and benchmarks. Discovered addresses are also collected once in a typed list for subsequent inspection.

## ASN targets and RIPE cache

Use `AS` followed by a decimal autonomous system number as a positional target. ASN targets can be mixed with IP addresses, CIDRs and other ASNs; lowercase `as` and leading zeros are normalized. These examples use documentation-only ASNs and prefixes:

```sh
find-nameservers -discover-only AS64496
find-nameservers -test-version -json AS64496 AS64497 192.0.2.0/24
```

ASN expansion uses [RIPEstat RIS Prefixes](https://stat.ripe.net/docs/data-api/api-endpoints/ris-prefixes.html) with `types=o`, `af=v4` and `list_prefixes=true`. Only originated IPv4 prefixes are included, not transited routes or a historical union of announcements. RIS supplies its latest available routing snapshot, which may lag current routing and is not a complete inventory. Explicit IPv6 IP/CIDR targets remain supported; ASN-derived IPv6 prefixes are excluded.

All target syntax is validated and all ASN lookups finish before DNS discovery starts. An unsuccessful/malformed API response, mismatched ASN, invalid prefix or missing IPv4 scope fails the command before scanning; a stale cache is never silently substituted after an API failure. IP/CIDR-only commands do not access RIPE. Requests have a 15s deadline and an 8MiB response-body limit; Ctrl+C also cancels lookup requests. Existing discovery and capability-check settings apply to the expanded scope.

Each canonical ASN is cached at `~/.cache/find-nameservers/ripe/AS<number>.json`, with the validated API response and freshness metadata in the same JSON file. The cache uses the home directory path shown here, independently of `XDG_CACHE_HOME`. Entries are replaced atomically with owner-only directory/file permissions. A cache I/O failure is reported on stderr; a valid API response can still be scanned if writing the cache fails.

Freshness defaults to eight hours when the API supplies no freshness lifetime. `Cache-Control: max-age` takes precedence over `Expires`, and explicit lifetimes can be shorter or longer than eight hours. `Date`, `Age` and request delay reduce the remaining lifetime. Invalid/conflicting freshness is treated as stale. `no-cache` forces a fresh request before reuse, and `no-store` prevents persistence/reuse of the response. `s-maxage` applies to shared caches and does not override this private cache. Expired, corrupt or incompatible entries are refreshed with a full request; reading an entry never extends its lifetime.

## Server inspection

Capability checks are opt-in. Without `-test-*` options, print discovered addresses only. When tests are selected, discovery finishes first and then each discovered server is inspected with only the selected tests. Inspection uses at most 8 concurrent server workers (`-check-workers`) and a 2s deadline per check (`-check-timeout`). Check failures are reported individually and never remove a discovered address. Detailed inspection can take longer than the roughly 2s discovery phase.

`-test-version` queries `version.bind. TXT CH` for self-reported version strings. Repeat `-test-recursion name` to compare non-recursive and recursive A queries for each supplied external name; no implicit recursion test is performed. Use names outside the target's authoritative zones; a controlled, previously uncached name gives stronger evidence. RA flags, authoritative answers, and cached answers alone do not establish public recursion. Reports describe behavior from this scanner's address, not access from every client.

```sh
find-nameservers -json -test-version -test-recursion probe.your-domain.example 192.0.2.0/24
find-nameservers -discover-only 192.0.2.0/24
```

`-json` outputs a JSON array with an address and independent check results, including statuses, evidence, and returned version strings. Selected tests produce human-readable reports by default. With no tests, `-json` produces address records with empty check lists. `-discover-only` emits one address per line and skips every capability check; it cannot be combined with `-json` or `-test-*` options. Benchmarks explicitly select this discovery-only mode. Timeouts and inconclusive answers are reported separately from refusals.

### Recursion evidence

`-test-recursion name` sends an RD=0 control first, reserving half the remaining check deadline for it, then sends RD=1 with the remaining time. A failed control is recorded and does not suppress the RD=1 probe; cancellation still stops the check. Supplied names are queried exactly, with no generated names or implicit extra targets.

Reports separate `recursion_advertised` (the valid RD=1 response's RA flag, or unknown if no usable response) from `recursion_observed` (whether this comparison established evidence of recursive answering). A false observed value means no evidence was established, not proof that recursion is impossible. RA=0 produces `not-available` even for authoritative answers: recursion was not advertised to this client for this query. Refusals, unsupported queries, failures and timeouts keep their own outcomes.

RA=1 alone, cached answers and authoritative answers remain inconclusive. A non-authoritative positive A answer (including CNAME chains), or NXDOMAIN with a relevant SOA, can provide recursive-answering evidence when the RD=0 control did not already supply data. A missing/failed control or an inconsistent response prevents that comparison from establishing recursion. This does not exclude cached or synthesized answers or establish access from every client.

Human and JSON reports retain each probe's flags, errors, and answer/authority records. Each probe retains at most 32 complete record strings and 8KiB of record text across both sections; omitted records set `evidence_limited`. Truncation and UDP-to-TCP fallback are recorded. Limits affect retained evidence, not response classification.

For stronger testing, use multiple independent names outside the target's locally served zones. Fresh names under a domain you control, with known positive or negative behavior and authoritative query logs, provide stronger evidence than a popular cached name. Random labels alone do not prove recursion: negative cache synthesis can answer without a new upstream query. Automatic fresh-name generation and log correlation are not performed.

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
