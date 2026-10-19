package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// A check is independent of discovery and can be registered without changing it.
type serverCheck interface {
	Run(context.Context, netip.AddrPort) checkResult
}

type extendedDNSError struct {
	Code uint16 `json:"code"`
	Text string `json:"text,omitempty"`
}

type observation struct {
	EDNS             *ednsObservation   `json:"edns,omitempty"`
	Authenticated    *bool              `json:"ad,omitempty"`
	CheckingDisabled *bool              `json:"cd,omitempty"`
	ExtendedErrors   []extendedDNSError `json:"extended_errors,omitempty"`
	Probe            string             `json:"probe,omitempty"`
	ResponseReceived *bool              `json:"response_received,omitempty"`
	Error            string             `json:"error,omitempty"`
	ErrorStatus      string             `json:"error_status,omitempty"`
	Truncated        bool               `json:"tc"`
	UDPTruncated     bool               `json:"udp_truncated,omitempty"`
	Answer           []string           `json:"answer,omitempty"`
	Authority        []string           `json:"authority,omitempty"`
	EvidenceLimited  bool               `json:"evidence_limited,omitempty"`
	Recursive        bool               `json:"rd"`
	Available        bool               `json:"ra"`
	Authoritative    bool               `json:"aa"`
	Rcode            string             `json:"rcode"`
	AnswerCount      int                `json:"answer_count"`
	AuthorityCount   int                `json:"authority_count"`
}

type checkResult struct {
	NSIDHex             *string       `json:"nsid_hex,omitempty"`
	RecursionAdvertised *bool         `json:"recursion_advertised,omitempty"`
	RecursionObserved   *bool         `json:"recursion_observed,omitempty"`
	Check               string        `json:"check"`
	Subject             string        `json:"subject,omitempty"`
	Status              string        `json:"status"`
	Detail              string        `json:"detail,omitempty"`
	Observations        []observation `json:"observations,omitempty"`
	Values              []string      `json:"values,omitempty"`
	Records             int           `json:"records,omitempty"`
	Bytes               int           `json:"bytes,omitempty"`
}

type serverReport struct {
	Address netip.Addr    `json:"address"`
	Checks  []checkResult `json:"checks"`
}

type inspectionOptions struct {
	workers int
	timeout time.Duration
	port    uint16 // injectable for loopback fixtures; CLI always uses 53
}

func (o inspectionOptions) validate() error {
	if o.workers < 1 || o.workers > 1024 {
		return errors.New("-check-workers must be between 1 and 1024")
	}
	if o.timeout <= 0 {
		return errors.New("-check-timeout must be positive")
	}
	return nil
}

func inspectServers(ctx context.Context, found []foundServer, checks []serverCheck, opts inspectionOptions) ([]serverReport, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	reports := make([]serverReport, len(found))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(opts.workers, len(found)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				report := serverReport{Address: found[index].Address, Checks: make([]checkResult, 0, len(checks))}
				for _, check := range checks {
					if ctx.Err() != nil {
						break
					}
					checkCtx, cancel := context.WithTimeout(ctx, opts.timeout)
					result := check.Run(checkCtx, netip.AddrPortFrom(report.Address, opts.port))
					cancel()
					report.Checks = append(report.Checks, result)
				}
				reports[index] = report
			}
		}()
	}
feed:
	for i := range found {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return reports, ctx.Err()
}

func queryDNS(ctx context.Context, target netip.AddrPort, q *dns.Msg) (*dns.Msg, error) {
	response, _, err := queryDNSObserved(ctx, target, q)
	return response, err
}

// Keep the UDP truncation signal and last received message even if TCP fallback
// fails, so recursion reports can explain each probe independently.
func queryDNSObserved(ctx context.Context, target netip.AddrPort, q *dns.Msg) (*dns.Msg, bool, error) {
	client := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	if deadline, ok := ctx.Deadline(); ok {
		client.Timeout = time.Until(deadline)
		if client.Timeout <= 0 {
			return nil, false, context.DeadlineExceeded
		}
	}
	response, err := exchangeDNS(ctx, client, q, target)
	if err != nil {
		return response, false, err
	}
	if !validResponse(q, response) {
		return response, false, errors.New("mismatched DNS response")
	}
	if !response.Truncated {
		return response, false, nil
	}
	client.Net = "tcp"
	tcp, err := exchangeDNS(ctx, client, q, target)
	if err != nil {
		return response, true, fmt.Errorf("TCP fallback: %w", err)
	}
	if !validResponse(q, tcp) || tcp.Truncated {
		return tcp, true, errors.New("invalid TCP DNS response")
	}
	return tcp, true, nil
}

// Explicit connection ownership makes cancellation interrupt a pending read,
// including cancellation without a deadline.
func exchangeDNS(ctx context.Context, client *dns.Client, q *dns.Msg, target netip.AddrPort) (*dns.Msg, error) {
	conn, err := client.DialContext(ctx, target.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	response, _, err := client.ExchangeWithConnContext(ctx, q, conn)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return response, err
}

func failureStatus(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout"
	}
	return "error"
}

func responseStatus(rcode int) string {
	switch rcode {
	case dns.RcodeRefused:
		return "refused"
	case dns.RcodeNotImplemented:
		return "unsupported"
	default:
		return "inconclusive"
	}
}

type versionCheck struct{}

func (versionCheck) Run(ctx context.Context, target netip.AddrPort) checkResult {
	result := checkResult{Check: "version", Subject: "version.bind.", Status: "inconclusive"}
	q := new(dns.Msg).SetQuestion("version.bind.", dns.TypeTXT)
	q.Question[0].Qclass = dns.ClassCHAOS
	q.RecursionDesired = false
	r, err := queryDNS(ctx, target, q)
	if err != nil {
		result.Status = failureStatus(err)
		result.Detail = err.Error()
		return result
	}
	result.Observations = []observation{observe(r)}
	if r.Rcode != dns.RcodeSuccess {
		result.Status = responseStatus(r.Rcode)
		result.Detail = dns.RcodeToString[r.Rcode]
		return result
	}
	for _, rr := range r.Answer {
		if txt, ok := rr.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, "version.bind.") && txt.Hdr.Class == dns.ClassCHAOS {
			result.Values = append(result.Values, strings.Join(txt.Txt, ""))
		}
	}
	if len(result.Values) > 0 {
		result.Status = "observed"
		result.Detail = "self-reported version TXT; not independently verified"
	} else {
		result.Detail = "no version TXT answer"
	}
	return result
}

func observe(r *dns.Msg) observation {
	return observation{Truncated: r.Truncated, Recursive: r.RecursionDesired, Available: r.RecursionAvailable, Authoritative: r.Authoritative, Rcode: dns.RcodeToString[r.Rcode], AnswerCount: len(r.Answer), AuthorityCount: len(r.Ns)}
}

func writeReports(output io.Writer, reports []serverReport, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(reports)
	}
	for _, report := range reports {
		if _, err := fmt.Fprintln(output, report.Address); err != nil {
			return err
		}
		for _, result := range report.Checks {
			if _, err := fmt.Fprintf(output, "  %s %s: %s", result.Check, result.Subject, result.Status); err != nil {
				return err
			}
			if result.Detail != "" {
				if _, err := fmt.Fprintf(output, " (%s)", result.Detail); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
			if result.NSIDHex != nil {
				if _, err := fmt.Fprintf(output, "    NSID hex: %q\n", *result.NSIDHex); err != nil {
					return err
				}
			}
			if result.Check == "dnssec" {
				if err := writeDNSSECEvidence(output, result); err != nil {
					return err
				}
			}
			if result.Check == "recursion" {
				if err := writeRecursionEvidence(output, result); err != nil {
					return err
				}
			}
			for _, value := range result.Values {
				if _, err := fmt.Fprintf(output, "    %q\n", value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// flag.Value appends rather than overwriting repeated options.
type namesFlag []string

func (values *namesFlag) String() string { return strings.Join(*values, ", ") }
func (values *namesFlag) Set(value string) error {
	if value == "." {
		*values = append(*values, value)
		return nil
	}
	if _, err := makeQuestion(value); err != nil {
		return err
	}
	*values = append(*values, dns.Fqdn(value))
	return nil
}
