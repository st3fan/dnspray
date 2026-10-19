package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func recursionA(name string) dns.RR {
	return &dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}
}
func recursionSOA(name string) dns.RR {
	return &dns.SOA{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeSOA, Class: dns.ClassINET}, Ns: "ns.example.", Mbox: "hostmaster.example.", Serial: 1}
}

func TestRecursionClassificationAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		mode, status         string
		advertised, observed bool
	}{
		{"ra0 aa1", "not-available", false, false},
		{"ra0 aa0", "not-available", false, false},
		{"ra1 aa1", "inconclusive", true, false},
		{"cached", "inconclusive", true, false},
		{"cached negative", "inconclusive", true, false},
		{"refused", "refused", false, false},
		{"unsupported", "unsupported", false, false},
		{"servfail", "inconclusive", true, false},
		{"control servfail", "inconclusive", true, false},
		{"positive", "observed", true, true},
		{"cname", "observed", true, true},
		{"negative", "observed", true, true},
		{"negative cname", "observed", true, true},
		{"negative wrong soa", "inconclusive", true, false},
		{"negative loop", "inconclusive", true, false},
		{"nodata", "inconclusive", true, false},
		{"ra only", "inconclusive", true, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.Question[0].Name != "probe.example." || q.Question[0].Qtype != dns.TypeA {
					t.Errorf("query was changed: %v", q)
				}
				r := new(dns.Msg).SetReply(q)
				r.RecursionAvailable = tc.advertised
				switch tc.mode {
				case "ra0 aa1", "ra1 aa1":
					r.Authoritative = true
					r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
				case "refused":
					r.Rcode = dns.RcodeRefused
				case "unsupported":
					r.Rcode = dns.RcodeNotImplemented
				case "servfail":
					r.Rcode = dns.RcodeServerFailure
				case "cached":
					r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
				case "cached negative":
					r.Rcode = dns.RcodeNameError
					r.Ns = []dns.RR{recursionSOA("example.")}
				case "control servfail":
					if !q.RecursionDesired {
						r.Rcode = dns.RcodeServerFailure
					} else {
						r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
					}
				default:
					if q.RecursionDesired {
						switch tc.mode {
						case "positive":
							r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
						case "cname":
							r.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: "terminal.example."}, recursionA("terminal.example.")}
						case "negative", "negative cname", "negative wrong soa", "negative loop", "nodata":
							r.Rcode = dns.RcodeNameError
							r.Ns = []dns.RR{recursionSOA("example.")}
							if tc.mode == "nodata" {
								r.Rcode = dns.RcodeSuccess
							}
							if tc.mode == "negative cname" {
								r.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: "terminal.example."}}
							}
							if tc.mode == "negative wrong soa" {
								r.Ns = []dns.RR{recursionSOA("unrelated.test.")}
							}
							if tc.mode == "negative loop" {
								r.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: q.Question[0].Name}}
							}
						}
					}
				}
				w.WriteMsg(r)
			})
			result := (recursionCheck{"probe.example."}).Run(context.Background(), target)
			if result.Status != tc.status || result.RecursionAdvertised == nil || *result.RecursionAdvertised != tc.advertised || result.RecursionObserved == nil || *result.RecursionObserved != tc.observed {
				t.Fatalf("%+v", result)
			}
			if len(result.Observations) != 2 {
				t.Fatal(result)
			}
			for i, probe := range result.Observations {
				if probe.ResponseReceived == nil || !*probe.ResponseReceived || probe.Recursive != (i == 1) || probe.Error != "" {
					t.Fatal(probe)
				}
			}
			probe := result.Observations[1]
			if tc.mode == "cname" && (len(probe.Answer) != 2 || !strings.Contains(probe.Answer[0], "CNAME")) {
				t.Fatal(probe)
			}
			if tc.mode == "negative" && (len(probe.Authority) != 1 || !strings.Contains(probe.Authority[0], "SOA")) {
				t.Fatal(probe)
			}
		})
	}
}

func TestIndependentRecursionProbes(t *testing.T) {
	for _, mode := range []string{"control timeout", "control malformed", "recursive malformed", "recursive timeout", "both timeout", "control timeout ra0", "control timeout refused", "rd mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var rd0, rd1 atomic.Int32
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.RecursionDesired {
					rd1.Add(1)
				} else {
					rd0.Add(1)
				}
				if mode == "both timeout" || (!q.RecursionDesired && strings.HasPrefix(mode, "control timeout")) || (q.RecursionDesired && mode == "recursive timeout") {
					return
				}
				r := new(dns.Msg).SetReply(q)
				r.RecursionAvailable = true
				if q.RecursionDesired {
					r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
				}
				if (mode == "control malformed" && !q.RecursionDesired) || (mode == "recursive malformed" && q.RecursionDesired) {
					r.Question = []dns.Question{{Name: "wrong.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET}}
				}
				if q.RecursionDesired && mode == "control timeout ra0" {
					r.RecursionAvailable = false
					r.Authoritative = true
				}
				if q.RecursionDesired && mode == "control timeout refused" {
					r.Rcode = dns.RcodeRefused
					r.RecursionAvailable = false
				}
				if q.RecursionDesired && mode == "rd mismatch" {
					r.RecursionDesired = false
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			result := (recursionCheck{"probe.example."}).Run(ctx, target)
			if rd0.Load() != 1 || rd1.Load() != 1 || len(result.Observations) != 2 {
				t.Fatalf("probes %d/%d: %+v", rd0.Load(), rd1.Load(), result)
			}
			want := "inconclusive"
			switch mode {
			case "recursive timeout", "both timeout":
				want = "timeout"
			case "recursive malformed", "rd mismatch":
				want = "error"
			case "control timeout ra0":
				want = "not-available"
			case "control timeout refused":
				want = "refused"
			}
			if result.Status != want || *result.RecursionObserved {
				t.Fatalf("want %s got %+v", want, result)
			}
			if strings.HasPrefix(mode, "control timeout") && (result.Observations[0].ErrorStatus != "timeout" || *result.Observations[0].ResponseReceived) {
				t.Fatal(result)
			}
			if (mode == "recursive malformed" || mode == "rd mismatch") && result.RecursionAdvertised != nil {
				t.Fatal("trusted invalid response flags")
			}
		})
	}
}

func TestRecursionCancellation(t *testing.T) {
	for _, cancelRD := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "recursive"}[cancelRD], func(t *testing.T) {
			received := make(chan struct{}, 1)
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.RecursionDesired == cancelRD {
					received <- struct{}{}
					return
				}
				w.WriteMsg(new(dns.Msg).SetReply(q))
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan checkResult, 1)
			go func() { done <- (recursionCheck{"probe.example."}).Run(ctx, target) }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("no probe")
			}
			cancel()
			select {
			case result := <-done:
				if result.Status != "canceled" {
					t.Fatal(result)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("pending probe did not cancel")
			}
		})
	}
}

func TestBoundedRecursionEvidence(t *testing.T) {
	r := new(dns.Msg)
	for range maxEvidenceRecords + 10 {
		r.Answer = append(r.Answer, recursionA("probe.example."))
	}
	r.Ns = []dns.RR{recursionSOA("example.")}
	answer, authority, limited := responseEvidence(r)
	if !limited || len(answer)+len(authority) != maxEvidenceRecords {
		t.Fatal(len(answer), len(authority), limited)
	}
	r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "probe.example.", Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{strings.Repeat("x", maxEvidenceBytes+1)}}, recursionA("probe.example.")}
	answer, authority, limited = responseEvidence(r)
	size := 0
	for _, s := range append(answer, authority...) {
		size += len(s)
	}
	if !limited || size > maxEvidenceBytes || len(answer) != 1 || len(authority) != 1 {
		t.Fatal(size, limited, answer, authority)
	}
}

func TestRecursionEvidenceOutput(t *testing.T) {
	result := checkResult{Check: "recursion", Subject: "probe.example.", Status: "not-available", RecursionAdvertised: boolValue(false), RecursionObserved: boolValue(false), Observations: []observation{{Probe: "RD=0", ResponseReceived: boolValue(false), ErrorStatus: "timeout", Error: "probe timed out"}, {Probe: "RD=1", ResponseReceived: boolValue(true), Recursive: true, Authoritative: true, Rcode: "NOERROR", Answer: []string{"probe.example. 60 IN A 192.0.2.1"}, Authority: []string{"example. 60 IN SOA ns.example. hostmaster.example. 1 60 60 3600 60"}, EvidenceLimited: true}}}
	reports := []serverReport{{Address: netip.MustParseAddr("192.0.2.1"), Checks: []checkResult{result}}}
	var output bytes.Buffer
	if err := writeReports(&output, reports, true); err != nil {
		t.Fatal(err)
	}
	var decoded []serverReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded[0].Checks[0]
	if got.RecursionAdvertised == nil || *got.RecursionAdvertised || got.RecursionObserved == nil || *got.RecursionObserved || len(got.Observations[1].Authority) != 1 || got.Observations[0].ErrorStatus != "timeout" {
		t.Fatal(output.String())
	}
	output.Reset()
	if err := writeReports(&output, reports, false); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"advertised: false", "observed: false", "RD=0", "timeout", "RD=1", "AA=true", "answer:", "authority:", "evidence limited"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatal(output.String())
		}
	}
	if err := writeRecursionEvidence(failingWriter{}, result); err == nil {
		t.Fatal("lost evidence output error")
	}
	result.RecursionAdvertised = nil
	output.Reset()
	if err := writeRecursionEvidence(&output, result); err != nil || !strings.Contains(output.String(), "advertised: unknown") {
		t.Fatal(output.String(), err)
	}
}

func TestRecursionTCPFallbackEvidence(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp4", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg).SetReply(q)
		r.RecursionAvailable = true
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			r.Truncated = true
		} else if q.RecursionDesired {
			r.Answer = []dns.RR{recursionA(q.Question[0].Name)}
		}
		w.WriteMsg(r)
	})
	for _, server := range []*dns.Server{{Listener: listener, Handler: handler}, {PacketConn: udp, Handler: handler}} {
		ready := make(chan struct{})
		server.NotifyStartedFunc = func() { close(ready) }
		done := make(chan error, 1)
		go func() { done <- server.ActivateAndServe() }()
		<-ready
		t.Cleanup(func() {
			server.Shutdown()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	result := (recursionCheck{"probe.example."}).Run(context.Background(), listener.Addr().(*net.TCPAddr).AddrPort())
	if result.Status != "observed" {
		t.Fatal(result)
	}
	for _, probe := range result.Observations {
		if !probe.UDPTruncated || probe.Truncated || probe.Error != "" {
			t.Fatal(probe)
		}
	}
}

// Check the byte-limited human writer beyond its first successful write.
type failAfterWriter struct{ remaining int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	w.remaining--
	return len(p), nil
}
func TestRecursionEvidenceWriteErrors(t *testing.T) {
	result := checkResult{Observations: []observation{{Probe: "RD=0", Error: "timeout", ErrorStatus: "timeout", Answer: []string{"A"}, Authority: []string{"SOA"}, EvidenceLimited: true}}}
	for n := 0; n < 6; n++ {
		if err := writeRecursionEvidence(&failAfterWriter{n}, result); err == nil {
			t.Fatalf("writer %d error lost", n)
		}
	}
}

func TestRecursionFallbackFailureEvidence(t *testing.T) {
	target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg).SetReply(q)
		r.Truncated = true
		w.WriteMsg(r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (recursionCheck{"probe.example."}).Run(ctx, target)
	if result.Status != "error" || result.RecursionAdvertised != nil || len(result.Observations) != 2 {
		t.Fatal(result)
	}
	for _, probe := range result.Observations {
		if !probe.UDPTruncated || !probe.Truncated || probe.ErrorStatus != "error" || !strings.Contains(probe.Error, "TCP fallback") {
			t.Fatal(probe)
		}
	}
}

func TestRecursionRejectsConflictingAliases(t *testing.T) {
	for _, mode := range []string{"address and alias", "conflicting aliases", "loop"} {
		t.Run(mode, func(t *testing.T) {
			r := new(dns.Msg)
			cname := func(from, to string) dns.RR {
				return &dns.CNAME{Hdr: dns.RR_Header{Name: from, Rrtype: dns.TypeCNAME, Class: dns.ClassINET}, Target: to}
			}
			r.Answer = []dns.RR{cname("probe.example.", "terminal.example."), recursionA("terminal.example.")}
			switch mode {
			case "address and alias":
				r.Answer = append(r.Answer, recursionA("probe.example."))
			case "conflicting aliases":
				r.Answer = append(r.Answer, cname("probe.example.", "other.example."))
			case "loop":
				r.Answer = []dns.RR{cname("probe.example.", "terminal.example."), cname("terminal.example.", "probe.example.")}
			}
			if answersA(r, "probe.example.") {
				t.Fatal("trusted inconsistent CNAME chain")
			}
		})
	}
}
