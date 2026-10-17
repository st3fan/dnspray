# find-nameservers

Scan every address in one or more IPv4 or IPv6 CIDRs for a DNS server on UDP port 53. Individual IP addresses are also accepted. Each responding address is printed on its own line.

```sh
go run . -h example.org -c 64 192.0.2.0/24 2001:db8::1
```

`-h` defaults to `www.example.com`; `-c` defaults to 32 concurrent queries. The tool sends an A query and waits up to two seconds per address. Any valid DNS response for that query counts, including `NXDOMAIN` or `REFUSED`.

The implementation uses only Go's standard library. Go's `net.Resolver` can perform lookups, but it does not expose every raw DNS response needed to determine whether a server answered.
