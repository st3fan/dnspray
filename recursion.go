package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const maxEvidenceRecords = 32
const maxEvidenceBytes = 8192

type recursionCheck struct{ name string }

func (check recursionCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "recursion", Subject: check.name, Status: "inconclusive", RecursionObserved: boolValue(false)}
	// Direct callers also get a bounded total deadline. The CLI supplies its own.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	deadline, _ := ctx.Deadline()
	// Reserve at least half the remaining budget for RD=1. A fast control leaves
	// its unused time available to the recursive probe.
	controlCtx, cancel := context.WithTimeout(ctx, time.Until(deadline)/2)
	control, controlEvidence, controlErr := recursionProbe(controlCtx, target, check.name, false)
	cancel()
	result.Observations = append(result.Observations, controlEvidence)
	if err := ctx.Err(); err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	recursive, recursiveEvidence, err := recursionProbe(ctx, target, check.name, true)
	result.Observations = append(result.Observations, recursiveEvidence)
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = "RD=1 probe: " + err.Error()
		return result
	}
	result.RecursionAdvertised = boolValue(recursive.RecursionAvailable)
	if recursive.Rcode == dns.RcodeRefused || recursive.Rcode == dns.RcodeNotImplemented {
		result.Status = responseStatus(recursive.Rcode)
		result.Detail = "RD=1 query returned " + dns.RcodeToString[recursive.Rcode]
		return result
	}
	if recursive.Rcode != dns.RcodeSuccess && recursive.Rcode != dns.RcodeNameError {
		result.Detail = "RD=1 returned " + dns.RcodeToString[recursive.Rcode] + "; no recursive answering established"
		return result
	}
	if !recursive.RecursionAvailable {
		result.Status = "not-available"
		result.Detail = "recursion not advertised to this client for this query (RA=0)"
		return result
	}
	if controlErr != nil {
		result.Detail = "recursion advertised, but RD=0 control failed: " + controlErr.Error() + "; comparison is inconclusive"
		return result
	}
	if recursive.Authoritative || control.Authoritative {
		result.Detail = "recursion advertised, but authoritative answers cannot establish recursive answering"
		return result
	}
	if control.Rcode == dns.RcodeNameError || (control.Rcode == dns.RcodeSuccess && (len(control.Answer) > 0 || negativeSOA(control, check.name))) {
		result.Detail = "RD=0 already supplied positive or negative data; cached data cannot establish recursive answering"
		return result
	}
	if control.Rcode != dns.RcodeSuccess && control.Rcode != dns.RcodeRefused {
		result.Detail = "recursion advertised, but RD=0 returned " + dns.RcodeToString[control.Rcode] + "; comparison is inconclusive"
		return result
	}
	positive := recursive.Rcode == dns.RcodeSuccess && answersA(recursive, check.name)
	negative := recursive.Rcode == dns.RcodeNameError && negativeSOA(recursive, check.name)
	if positive || negative {
		result.Status = "observed"
		result.RecursionObserved = boolValue(true)
		result.Detail = "recursive answering observed from this scanner: RA=1 and a terminal RD=1 answer where RD=0 did not answer; cached or synthesized data cannot be excluded without controlled-zone logs"
	} else {
		result.Detail = "recursion advertised, but no terminal recursive answer established; RA alone is insufficient"
	}
	return result
}

func boolValue(value bool) *bool { return &value }

func recursionProbe(ctx context.Context, target netip.AddrPort, name string, rd bool) (*dns.Msg, observation, error) {
	q := new(dns.Msg).SetQuestion(name, dns.TypeA)
	q.RecursionDesired = rd
	response, udpTruncated, err := queryDNSObserved(ctx, target, q)
	evidence := observation{Recursive: rd, ResponseReceived: boolValue(response != nil), UDPTruncated: udpTruncated}
	if response != nil {
		evidence = observe(response)
		evidence.ResponseReceived = boolValue(true)
		evidence.UDPTruncated = udpTruncated
		evidence.Answer, evidence.Authority, evidence.EvidenceLimited = responseEvidence(response)
		if err == nil && response.RecursionDesired != rd {
			err = fmt.Errorf("response RD flag does not match the probe")
		}
	}
	evidence.Probe = "RD=0"
	if rd {
		evidence.Probe = "RD=1"
	}
	if err != nil {
		evidence.Error = err.Error()
		evidence.ErrorStatus = failureStatus(err)
	}
	return response, evidence, err
}

// Evidence limits apply across both sections for each probe, retaining complete
// RR strings only. Classification uses the full validated response.
func responseEvidence(response *dns.Msg) (answer, authority []string, limited bool) {
	remainingRecords, remainingBytes := maxEvidenceRecords, maxEvidenceBytes
	sections := []*[]string{&answer, &authority}
	for i, records := range [][]dns.RR{response.Answer, response.Ns} {
		for _, record := range records {
			if remainingRecords == 0 {
				limited = true
				continue
			}
			value := record.String()
			if len(value) > remainingBytes {
				limited = true
				continue
			}
			*sections[i] = append(*sections[i], value)
			remainingRecords--
			remainingBytes -= len(value)
		}
	}
	return
}

// NXDOMAIN and NODATA require an enclosing SOA to count as negative evidence.
// It is evidence of a terminal response, not proof of a cache miss.
func negativeSOA(response *dns.Msg, name string) bool {
	if response.Rcode != dns.RcodeNameError && response.Rcode != dns.RcodeSuccess {
		return false
	}
	if answersA(response, name) {
		return false
	}
	terminal, ok := canonicalName(response, name)
	if !ok {
		return false
	}
	for _, record := range response.Ns {
		if soa, ok := record.(*dns.SOA); ok && soa.Hdr.Class == dns.ClassINET && dns.IsSubDomain(soa.Hdr.Name, terminal) {
			return true
		}
	}
	return false
}

func canonicalName(response *dns.Msg, name string) (string, bool) {
	name = dns.Fqdn(name)
	visited := make(map[string]bool)
	for range len(response.Answer) + 1 {
		key := strings.ToLower(name)
		if visited[key] {
			return "", false
		}
		visited[key] = true
		next := ""
		hasAddress := false
		for _, record := range response.Answer {
			if record.Header().Class == dns.ClassINET && strings.EqualFold(record.Header().Name, name) {
				switch record.(type) {
				case *dns.A, *dns.AAAA:
					hasAddress = true
				}
			}
			if cname, ok := record.(*dns.CNAME); ok && cname.Hdr.Class == dns.ClassINET && strings.EqualFold(cname.Hdr.Name, name) {
				if next != "" && !strings.EqualFold(next, cname.Target) {
					return "", false
				}
				next = cname.Target
			}
		}
		if next == "" {
			return name, true
		}
		if hasAddress {
			return "", false
		}
		name = next
	}
	return "", false
}

func writeRecursionEvidence(output io.Writer, result checkResult) error {
	advertised := "unknown"
	if result.RecursionAdvertised != nil {
		advertised = fmt.Sprint(*result.RecursionAdvertised)
	}
	observed := result.RecursionObserved != nil && *result.RecursionObserved
	if _, err := fmt.Fprintf(output, "    recursion advertised: %s; recursive answering observed: %t\n", advertised, observed); err != nil {
		return err
	}
	for _, probe := range result.Observations {
		received := probe.ResponseReceived != nil && *probe.ResponseReceived
		if _, err := fmt.Fprintf(output, "    %s: response=%t RA=%t AA=%t TC=%t UDP-truncated=%t RCODE=%s\n", probe.Probe, received, probe.Available, probe.Authoritative, probe.Truncated, probe.UDPTruncated, probe.Rcode); err != nil {
			return err
		}
		if probe.Error != "" {
			if _, err := fmt.Fprintf(output, "      %s: %q\n", probe.ErrorStatus, probe.Error); err != nil {
				return err
			}
		}
		for i, section := range [][]string{probe.Answer, probe.Authority} {
			label := "answer"
			if i == 1 {
				label = "authority"
			}
			for _, record := range section {
				if _, err := fmt.Fprintf(output, "      %s: %q\n", label, record); err != nil {
					return err
				}
			}
		}
		if probe.EvidenceLimited {
			if _, err := fmt.Fprintln(output, "      evidence limited"); err != nil {
				return err
			}
		}
	}
	return nil
}
func answersA(response *dns.Msg, name string) bool {
	terminal, ok := canonicalName(response, name)
	if !ok {
		return false
	}
	for _, record := range response.Answer {
		if _, ok := record.(*dns.A); ok && record.Header().Class == dns.ClassINET && strings.EqualFold(record.Header().Name, terminal) {
			return true
		}
	}
	return false
}
