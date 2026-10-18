# find-nameservers

Scan every address in one or more IPv4 or IPv6 CIDRs for a DNS server on UDP port 53. Individual IP addresses are also accepted. Each responding address is printed on its own line.

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

`-h` defaults to `www.example.com`. The scanner sends three UDP A queries per address with at least one second between attempts by default, using batches of up to 1,024 addresses. Replies are collected concurrently on one shared socket per address family. Each responding address is printed once. Any valid matching DNS response counts, including `NXDOMAIN` or `REFUSED`.

`-rate` sets a global maximum send rate (default 100 packets/second), shared across targets, retries, and both address families. `-rate 0` disables pacing for experiments. `-retry-gap` sets the positive minimum delay between attempts to the same address (default `1s`; accepts Go durations such as `1s`). Actual delays may be longer because of pacing and batch scheduling. Delayed sends do not accumulate credit for a catch-up burst.

`-t` is a positive number of seconds (default 2) to keep listening **after the final request is sent**. Listening also runs throughout sending. Total runtime includes sending, retry delays, and this final grace period. Ctrl+C stops sending and listening. The former `-c` worker-concurrency flag has been removed for this experiment.

Sending and retry storage are bounded; remembered discoveries grow with the number of responding addresses. Overlapping prefixes may cause additional probes, but duplicate replies produce only one output line. Send, receive, and output errors fail the scan visibly. High-speed UDP probing can lose packets, so compare discovered servers as well as elapsed time.

The implementation uses [`github.com/miekg/dns`](https://github.com/miekg/dns) to construct queries, exchange DNS messages over UDP, and parse responses.
