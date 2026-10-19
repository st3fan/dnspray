package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func udpFixture(t *testing.T, handler dns.HandlerFunc) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: handler}
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
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

func TestVersionCheck(t *testing.T) {
	for _, mode := range []string{"txt", "refused", "empty", "wrong class", "wrong question", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.Question[0].Name != "version.bind." || q.Question[0].Qtype != dns.TypeTXT || q.Question[0].Qclass != dns.ClassCHAOS || q.RecursionDesired {
					t.Error("invalid version query")
				}
				r := new(dns.Msg).SetReply(q)
				switch mode {
				case "txt":
					r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "version.bind.", Rrtype: dns.TypeTXT, Class: dns.ClassCHAOS}, Txt: []string{"test", " version"}}}
				case "refused":
					r.Rcode = dns.RcodeRefused
				case "wrong class":
					r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "version.bind.", Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"wrong"}}}
				case "wrong question":
					r.Question[0].Name = "wrong.example."
				case "truncated":
					r.Truncated = true
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			got := (versionCheck{}).Run(ctx, target)
			want := map[string]string{"txt": "observed", "refused": "refused", "empty": "inconclusive", "wrong class": "inconclusive", "wrong question": "error", "truncated": "error"}[mode]
			if got.Status != want {
				t.Fatalf("%+v", got)
			}
			if mode == "txt" && (len(got.Values) != 1 || got.Values[0] != "test version") {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRecursionEvidence(t *testing.T) {
	for _, mode := range []string{"recursive", "cached", "ra only", "authoritative", "refused", "unrelated", "nxdomain"} {
		t.Run(mode, func(t *testing.T) {
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				r := new(dns.Msg).SetReply(q)
				r.RecursionAvailable = true
				if mode == "authoritative" {
					r.Authoritative = true
				}
				if mode == "refused" {
					r.Rcode = dns.RcodeRefused
				}
				if mode == "nxdomain" {
					r.Rcode = dns.RcodeNameError
				}
				if mode == "cached" || ((mode == "recursive" || mode == "authoritative" || mode == "unrelated") && q.RecursionDesired) {
					name := q.Question[0].Name
					if mode == "unrelated" {
						name = "unrelated.example."
					}
					r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
				}
				w.WriteMsg(r)
			})
			got := (recursionCheck{name: "probe.example."}).Run(context.Background(), target)
			want := "inconclusive"
			if mode == "recursive" {
				want = "observed"
			}
			if mode == "refused" {
				want = "refused"
			}
			if got.Status != want || len(got.Observations) != 2 || got.Observations[0].Recursive || !got.Observations[1].Recursive {
				t.Fatalf("%+v", got)
			}
		})
	}
}

type fixtureCheck struct {
	active, max, calls *atomic.Int32
	block              bool
	status             string
}

func (f fixtureCheck) Run(ctx context.Context, _ netip.AddrPort) checkResult {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	f.calls.Add(1)
	for {
		old := f.max.Load()
		if n <= old || f.max.CompareAndSwap(old, n) {
			break
		}
	}
	if f.block {
		<-ctx.Done()
	} else {
		time.Sleep(time.Millisecond)
	}
	return checkResult{Check: "fixture", Status: f.status}
}

func TestInspectionBoundsIsolationAndCancellation(t *testing.T) {
	var active, max, calls atomic.Int32
	found := make([]foundServer, 9)
	for i := range found {
		found[i].Address = netip.MustParseAddr("127.0.0.1")
	}
	checks := []serverCheck{fixtureCheck{&active, &max, &calls, true, "timeout"}, fixtureCheck{&active, &max, &calls, false, "observed"}}
	opts := inspectionOptions{workers: 3, timeout: 10 * time.Millisecond, port: 53}
	reports, err := inspectServers(context.Background(), found, checks, opts)
	if err != nil || len(reports) != 9 || calls.Load() != 18 || max.Load() > 3 {
		t.Fatalf("reports=%v calls=%d max=%d err=%v", reports, calls.Load(), max.Load(), err)
	}
	for _, report := range reports {
		if len(report.Checks) != 2 || report.Checks[0].Status != "timeout" || report.Checks[1].Status != "observed" {
			t.Fatal(report)
		}
	}
	calls.Store(0)
	reports, err = inspectServers(context.Background(), nil, checks, opts)
	if err != nil || len(reports) != 0 || calls.Load() != 0 {
		t.Fatal("inspected an undiscovered server")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = inspectServers(ctx, found, checks, opts)
	if err != context.Canceled || active.Load() != 0 {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestCheckTimeout(t *testing.T) {
	target := udpFixture(t, func(dns.ResponseWriter, *dns.Msg) {})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := (versionCheck{}).Run(ctx, target)
	if result.Status != "timeout" {
		t.Fatalf("%+v", result)
	}
}

func TestReportOutput(t *testing.T) {
	reports := []serverReport{{Address: netip.MustParseAddr("192.0.2.1"), Checks: []checkResult{{Check: "version", Status: "observed", Values: []string{"line\n\"quoted\""}}}}}
	var output bytes.Buffer
	if err := writeReports(&output, reports, true); err != nil {
		t.Fatal(err)
	}
	var decoded []serverReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || decoded[0].Checks[0].Values[0] != reports[0].Checks[0].Values[0] {
		t.Fatal(err)
	}
	output.Reset()
	if err := writeReports(&output, reports, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `line\n\"quoted\"`) {
		t.Fatal(output.String())
	}
	if err := writeReports(failingWriter{}, reports, false); err == nil {
		t.Fatal("lost output error")
	}
	if err := writeReports(failingWriter{}, reports, true); err == nil {
		t.Fatal("lost JSON output error")
	}
	output.Reset()
	if err := writeReports(&output, []serverReport{}, true); err != nil || strings.TrimSpace(output.String()) != "[]" {
		t.Fatal(output.String(), err)
	}
}

func TestRepeatedNamesFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var names namesFlag
	fs.Var(&names, "test-recursion", "")
	if err := fs.Parse([]string{"-test-recursion", "one.example", "-test-recursion", "two.example."}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "one.example." || names[1] != "two.example." {
		t.Fatal(names)
	}
	if names.Set("bad..name") == nil {
		t.Fatal("accepted invalid name")
	}
}

func TestQueryCancellationInterruptsRead(t *testing.T) {
	received := make(chan struct{}, 1)
	target := udpFixture(t, func(dns.ResponseWriter, *dns.Msg) { received <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan checkResult, 1)
	go func() { done <- (versionCheck{}).Run(ctx, target) }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("no query")
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != "canceled" {
			t.Fatal(result)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("read did not cancel")
	}
}
