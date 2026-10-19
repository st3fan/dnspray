package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const defaultAXFRRecords = 10000
const defaultAXFRBytes = 1 << 20

type axfrCheck struct {
	zone       string
	maxRecords int
	maxBytes   int
}

func axfrChecks(zones []string, maxRecords, maxBytes int) []serverCheck {
	if len(zones) == 0 {
		return nil
	}
	checks := make([]serverCheck, 0, len(zones))
	for _, zone := range zones {
		checks = append(checks, axfrCheck{zone, maxRecords, maxBytes})
	}
	return checks
}

// AXFR is streamed and counted, never accumulated as an in-memory zone dump.
func (check axfrCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "axfr", Subject: check.zone, Status: "skipped", Detail: "no -test-axfr supplied"}
	if check.zone == "" {
		return result
	}
	result.Status = "error"
	result.Detail = ""
	if check.maxRecords < 2 || check.maxBytes < 512 {
		result.Detail = "invalid AXFR limits"
		return result
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target.String())
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		result.Detail = err.Error()
		return result
	}
	q := new(dns.Msg).SetAxfr(check.zone)
	if err := (&dns.Conn{Conn: conn}).WriteMsg(q); err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	var opening *dns.SOA
	fail := func(err error) checkResult {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		result.Status = failureStatus(err)
		if opening != nil && result.Status != "canceled" {
			result.Status = "partial"
		}
		result.Detail = err.Error()
		return result
	}
	first := true
	for {
		var header [2]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return fail(err)
		}
		size := int(binary.BigEndian.Uint16(header[:]))
		if result.Bytes+size+2 > check.maxBytes {
			result.Status = "limited"
			result.Detail = "AXFR byte limit reached before completion"
			return result
		}
		result.Bytes += size + 2
		packet := make([]byte, size)
		if _, err := io.ReadFull(conn, packet); err != nil {
			return fail(err)
		}
		r := new(dns.Msg)
		if err := r.Unpack(packet); err != nil {
			return fail(err)
		}
		if r.Id != q.Id || !r.Response || r.Opcode != dns.OpcodeQuery || r.Truncated {
			return fail(errors.New("invalid AXFR response header"))
		}
		// Only the first transfer message must repeat the question.
		if (first || len(r.Question) > 0) && !validResponse(q, r) {
			return fail(errors.New("invalid AXFR response question"))
		}
		first = false
		if r.Rcode != dns.RcodeSuccess {
			if result.Records > 0 {
				return fail(fmt.Errorf("AXFR interrupted by %s", dns.RcodeToString[r.Rcode]))
			}
			result.Status = responseStatus(r.Rcode)
			result.Detail = dns.RcodeToString[r.Rcode]
			return result
		}
		if len(r.Answer) == 0 {
			return fail(errors.New("empty AXFR answer"))
		}
		for i, rr := range r.Answer {
			if result.Records >= check.maxRecords {
				result.Status = "limited"
				result.Detail = "AXFR record limit reached before completion"
				return result
			}
			result.Records++
			soa, isSOA := rr.(*dns.SOA)
			if opening == nil {
				if !isSOA || !strings.EqualFold(soa.Hdr.Name, check.zone) || soa.Hdr.Class != dns.ClassINET {
					return fail(errors.New("AXFR must open with the requested zone SOA"))
				}
				opening = soa
				continue
			}
			if rr.Header().Class != dns.ClassINET || !dns.IsSubDomain(check.zone, rr.Header().Name) {
				return fail(errors.New("AXFR record is outside the requested zone or class"))
			}
			if isSOA && strings.EqualFold(soa.Hdr.Name, check.zone) {
				if !sameSOA(opening, soa) || i != len(r.Answer)-1 {
					return fail(errors.New("invalid closing AXFR SOA"))
				}
				result.Status = "observed"
				result.Detail = "complete AXFR allowed from this scanner; zone contents not retained"
				return result
			}
		}
	}
}

func sameSOA(a, b *dns.SOA) bool {
	return strings.EqualFold(a.Hdr.Name, b.Hdr.Name) && a.Hdr.Class == b.Hdr.Class && a.Hdr.Ttl == b.Hdr.Ttl &&
		strings.EqualFold(a.Ns, b.Ns) && strings.EqualFold(a.Mbox, b.Mbox) && a.Serial == b.Serial && a.Refresh == b.Refresh && a.Retry == b.Retry && a.Expire == b.Expire && a.Minttl == b.Minttl
}
