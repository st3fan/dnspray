package main

import (
	"context"
	"encoding/hex"
	"net/netip"

	"github.com/miekg/dns"
)

type ednsObservation struct {
	Version  uint8  `json:"version"`
	UDPSize  uint16 `json:"udp_size"`
	DNSSECOK bool   `json:"do"`
}

type nsidCheck struct{}

func (nsidCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "nsid", Subject: ".", Status: "inconclusive"}
	q := new(dns.Msg).SetQuestion(".", dns.TypeNS)
	q.RecursionDesired = false
	q.SetEdns0(1232, false)
	q.IsEdns0().Option = append(q.IsEdns0().Option, &dns.EDNS0_NSID{Code: dns.EDNS0NSID})
	r, err := queryDNS(ctx, target, q)
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	evidence := observe(r)
	opt := r.IsEdns0()
	if opt != nil {
		evidence.EDNS = &ednsObservation{opt.Version(), opt.UDPSize(), opt.Do()}
	}
	result.Observations = []observation{evidence}
	if r.Rcode == dns.RcodeFormatError || r.Rcode == dns.RcodeBadVers || r.Rcode == dns.RcodeNotImplemented || (opt != nil && opt.Version() != 0) {
		result.Status = "edns-rejected"
		result.Detail = "EDNS request rejected or unsupported EDNS version returned"
		return result
	}
	for _, rr := range r.Extra {
		other, ok := rr.(*dns.OPT)
		if !ok {
			continue
		}
		if other != opt {
			result.Status = "error"
			result.Detail = "multiple EDNS OPT records"
			return result
		}
		for _, option := range other.Option {
			if nsid, ok := option.(*dns.EDNS0_NSID); ok {
				if result.NSIDHex != nil {
					result.Status = "error"
					result.Detail = "multiple NSID options"
					result.NSIDHex = nil
					return result
				}
				raw, err := hex.DecodeString(nsid.Nsid)
				if err != nil {
					result.Status = "error"
					result.Detail = "malformed NSID encoding"
					return result
				}
				encoded := hex.EncodeToString(raw)
				result.NSIDHex = &encoded
			}
		}
	}
	if result.NSIDHex != nil {
		result.Status = "observed"
		result.Detail = "opaque server identifier returned; not verified as stable or unique"
	} else if r.Rcode == dns.RcodeRefused {
		result.Status = "refused"
		result.Detail = "query refused without an NSID option"
	} else {
		result.Detail = "valid response without an NSID option; support may depend on server policy"
	}
	return result
}
