package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/miekg/dns"
)

type dnssecCheck struct{ valid, bogus string }

func (check dnssecCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "dnssec", Subject: check.valid + " / " + check.bogus, Status: "inconclusive"}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	var replies [3]*dns.Msg
	var failures [3]error
	probes := []struct {
		name, label string
		cd          bool
	}{
		{check.valid, "valid CD=0", false},
		{check.bogus, "bogus CD=0", false},
		{check.bogus, "bogus CD=1", true},
	}
	for i, probe := range probes {
		if err := ctx.Err(); err != nil {
			result.Status = failureStatus(err)
			result.Detail = err.Error()
			return result
		}
		// A slow or unavailable probe must leave time for the remaining controls.
		probeCtx, cancel := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(probes)-i))
		q := new(dns.Msg).SetQuestion(probe.name, dns.TypeA)
		q.RecursionDesired = true
		q.CheckingDisabled = probe.cd
		q.AuthenticatedData = true
		q.SetEdns0(1232, true)
		r, truncated, err := queryDNSObserved(probeCtx, target, q)
		cancel()
		evidence := observation{Probe: probe.label, ResponseReceived: boolValue(r != nil), UDPTruncated: truncated}
		if r != nil {
			evidence = observe(r)
			evidence.Probe = probe.label
			evidence.ResponseReceived = boolValue(true)
			evidence.UDPTruncated = truncated
			evidence.Authenticated = boolValue(r.AuthenticatedData)
			evidence.CheckingDisabled = boolValue(r.CheckingDisabled)
			evidence.Answer, evidence.Authority, evidence.EvidenceLimited = responseEvidence(r)
			if opt := r.IsEdns0(); opt != nil {
				remaining := maxEvidenceBytes
				for _, option := range opt.Option {
					if ede, ok := option.(*dns.EDNS0_EDE); ok {
						text := ede.ExtraText
						if len(evidence.ExtendedErrors) >= maxEvidenceRecords {
							evidence.EvidenceLimited = true
							continue
						}
						if len(text) > remaining {
							text = text[:remaining]
							evidence.EvidenceLimited = true
						}
						remaining -= len(text)
						evidence.ExtendedErrors = append(evidence.ExtendedErrors, extendedDNSError{ede.InfoCode, text})
					}
				}
			}
			if err == nil && (!r.RecursionDesired || r.CheckingDisabled != probe.cd) {
				err = fmt.Errorf("response RD/CD flags do not match the probe")
			}
		}
		if err != nil {
			evidence.Error = err.Error()
			evidence.ErrorStatus = failureStatus(err)
		}
		result.Observations = append(result.Observations, evidence)
		replies[i], failures[i] = r, err
	}
	for i, err := range failures {
		if err != nil {
			result.Status = failureStatus(err)
			result.Detail = probes[i].label + ": " + err.Error() + "; validation comparison inconclusive"
			return result
		}
	}
	valid, bogus, control := replies[0], replies[1], replies[2]
	for _, r := range replies {
		if r.Rcode == dns.RcodeRefused || r.Rcode == dns.RcodeNotImplemented {
			result.Status = responseStatus(r.Rcode)
			result.Detail = "probe rejected; validation comparison inconclusive"
			return result
		}
		if !r.RecursionAvailable || r.Authoritative {
			result.Detail = "recursive, non-authoritative responses required for validation comparison"
			return result
		}
	}
	if valid.Rcode != dns.RcodeSuccess || !answersA(valid, check.valid) || !valid.AuthenticatedData {
		result.Detail = "valid signed name did not return an authenticated positive answer"
		return result
	}
	if control.Rcode != dns.RcodeSuccess || !answersA(control, check.bogus) || control.AuthenticatedData {
		result.Detail = "checking-disabled bogus-name control did not return an unauthenticated positive answer"
		return result
	}
	switch {
	case bogus.Rcode == dns.RcodeServerFailure && !bogus.AuthenticatedData && len(bogus.Answer) == 0:
		result.Status = "observed"
		result.Detail = "DNSSEC validation behavior observed: authenticated valid answer, bogus SERVFAIL, and successful CD=1 control; not a cryptographic audit"
	case bogus.Rcode == dns.RcodeSuccess && answersA(bogus, check.bogus) && !bogus.AuthenticatedData:
		result.Status = "not-observed"
		result.Detail = "known bogus name answered with checking enabled; validation not observed for these probes"
	default:
		result.Detail = "unexpected bogus-name response; validation comparison inconclusive"
	}
	return result
}

func writeDNSSECEvidence(out io.Writer, result checkResult) error {
	for _, p := range result.Observations {
		ad, cd := false, false
		if p.Authenticated != nil {
			ad = *p.Authenticated
		}
		if p.CheckingDisabled != nil {
			cd = *p.CheckingDisabled
		}
		if _, err := fmt.Fprintf(out, "    %s: RA=%t AA=%t AD=%t CD=%t RCODE=%s\n", p.Probe, p.Available, p.Authoritative, ad, cd, p.Rcode); err != nil {
			return err
		}
		if p.Error != "" {
			if _, err := fmt.Fprintf(out, "      %s: %q\n", p.ErrorStatus, p.Error); err != nil {
				return err
			}
		}
		for _, ede := range p.ExtendedErrors {
			if _, err := fmt.Fprintf(out, "      EDE %d: %q\n", ede.Code, ede.Text); err != nil {
				return err
			}
		}
	}
	return nil
}
