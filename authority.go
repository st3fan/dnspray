package main

import (
	"context"
	"net/netip"

	"github.com/miekg/dns"
)

type authorityCheck struct{ name string }

func (check authorityCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "authority", Subject: check.name, Status: "inconclusive"}
	q := new(dns.Msg).SetQuestion(check.name, dns.TypeSOA)
	q.RecursionDesired = false
	r, err := queryDNS(ctx, target, q)
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	result.Observations = []observation{observe(r)}
	for _, section := range [][]dns.RR{r.Answer, r.Ns} {
		for _, rr := range section {
			if soa, ok := rr.(*dns.SOA); ok && soa.Hdr.Class == dns.ClassINET {
				result.Values = append(result.Values, soa.String())
			}
		}
	}
	if r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError {
		result.Status = responseStatus(r.Rcode)
		result.Detail = dns.RcodeToString[r.Rcode]
		return result
	}
	if r.Authoritative {
		result.Status = "observed"
		result.Detail = "authoritative answer for the queried domain (AA=1); does not establish that this name is a zone apex"
	} else {
		result.Status = "not-authoritative"
		result.Detail = "response was not authoritative for this query (AA=0)"
	}
	return result
}
