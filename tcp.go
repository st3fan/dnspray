package main

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/miekg/dns"
)

type tcpCheck struct{}

func (tcpCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "tcp", Subject: ".", Status: "inconclusive"}
	q := new(dns.Msg).SetQuestion(".", dns.TypeNS)
	q.RecursionDesired = false
	r, err := exchangeDNS(ctx, &dns.Client{Net: "tcp", Timeout: 2 * time.Second}, q, target)
	if err == nil && (!validResponse(q, r) || r.Truncated) {
		err = errors.New("invalid TCP DNS response")
	}
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	result.Status = "observed"
	result.Detail = "matching DNS response over TCP; response RCODE does not imply query success"
	result.Observations = []observation{observe(r)}
	return result
}
