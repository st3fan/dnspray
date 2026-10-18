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
find-nameservers -h example.org -c 64 192.0.2.0/24 2001:db8::1
```

`-h` defaults to `www.example.com`; `-c` defaults to 32 concurrent queries. The tool sends an A query and waits up to two seconds per address. Any valid DNS response for that query counts, including `NXDOMAIN` or `REFUSED`.

The implementation uses [`github.com/miekg/dns`](https://github.com/miekg/dns) to construct queries, exchange DNS messages over UDP, and parse responses.
