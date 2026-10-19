package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func tcpFixture(t *testing.T, handler dns.HandlerFunc) netip.AddrPort {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, Handler: handler}
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
	return listener.Addr().(*net.TCPAddr).AddrPort()
}
func zoneSOA(zone string) *dns.SOA {
	return &dns.SOA{Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60}, Ns: "ns." + zone, Mbox: "hostmaster." + zone, Serial: 1, Refresh: 60, Retry: 60, Expire: 3600, Minttl: 60}
}
func axfrMessage(q *dns.Msg, rrs ...dns.RR) *dns.Msg {
	r := new(dns.Msg).SetReply(q)
	r.Authoritative = true
	r.Answer = rrs
	return r
}

func TestAXFRTransferOutcomes(t *testing.T) {
	for _, mode := range []string{"complete", "one frame", "refused", "unsupported", "partial", "wrong opening", "wrong close", "trailing data", "wrong question", "wrong ID", "outside zone", "record limit", "byte limit", "timeout", "empty", "truncated", "late refusal", "inconsistent class"} {
		t.Run(mode, func(t *testing.T) {
			target := tcpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if len(q.Question) != 1 || q.Question[0].Qtype != dns.TypeAXFR || q.RecursionDesired {
					t.Error("invalid AXFR query")
				}
				soa := zoneSOA(q.Question[0].Name)
				address := &dns.A{Hdr: dns.RR_Header{Name: "www." + q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}
				switch mode {
				case "timeout":
					time.Sleep(80 * time.Millisecond)
					return
				case "refused", "unsupported":
					r := axfrMessage(q)
					r.Rcode = dns.RcodeRefused
					if mode == "unsupported" {
						r.Rcode = dns.RcodeNotImplemented
					}
					w.WriteMsg(r)
					return
				case "empty":
					w.WriteMsg(axfrMessage(q))
					return
				case "wrong opening":
					w.WriteMsg(axfrMessage(q, address))
					return
				case "one frame":
					w.WriteMsg(axfrMessage(q, soa, address, soa))
					return
				case "wrong question":
					r := axfrMessage(q, soa)
					r.Question = []dns.Question{{Name: "wrong.example.", Qtype: dns.TypeAXFR, Qclass: dns.ClassINET}}
					w.WriteMsg(r)
					return
				case "wrong ID":
					r := axfrMessage(q, soa)
					r.Id ^= 1
					w.WriteMsg(r)
					return
				case "truncated":
					r := axfrMessage(q, soa)
					r.Truncated = true
					w.WriteMsg(r)
					return
				}
				w.WriteMsg(axfrMessage(q, soa))
				if mode == "partial" {
					w.Close()
					return
				}
				if mode == "late refusal" {
					r := axfrMessage(q)
					r.Rcode = dns.RcodeRefused
					w.WriteMsg(r)
					return
				}
				closing := *soa
				if mode == "wrong close" {
					closing.Serial++
				}
				if mode == "outside zone" {
					address.Hdr.Name = "outside.invalid."
				}
				if mode == "inconsistent class" {
					address.Hdr.Class = dns.ClassCHAOS
				}
				r := axfrMessage(q, address, &closing)
				r.Question = nil
				if mode == "trailing data" {
					r.Answer = append(r.Answer, address)
				}
				if mode == "byte limit" {
					r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "large." + q.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{string(make([]byte, 250)), string(make([]byte, 250)), string(make([]byte, 250))}}, &closing}
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			check := axfrCheck{zone: "example.test.", maxRecords: 100, maxBytes: 4096}
			if mode == "record limit" {
				check.maxRecords = 2
			}
			if mode == "byte limit" {
				check.maxBytes = 512
			}
			got := check.Run(ctx, target)
			want := map[string]string{"complete": "observed", "one frame": "observed", "refused": "refused", "unsupported": "unsupported", "partial": "partial", "wrong opening": "error", "wrong close": "partial", "trailing data": "partial", "wrong question": "error", "wrong ID": "error", "outside zone": "partial", "record limit": "limited", "byte limit": "limited", "timeout": "timeout", "empty": "error", "truncated": "error", "late refusal": "partial", "inconsistent class": "partial"}[mode]
			if got.Status != want {
				t.Fatalf("got %+v want %s", got, want)
			}
			if want == "observed" && got.Records != 3 {
				t.Fatal(got)
			}
		})
	}
}

func TestRepeatedAXFRZonesThroughInspectionAndJSON(t *testing.T) {
	fs := flag.NewFlagSet("inspection", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var tests testOptions
	tests.register(fs)
	if err := fs.Parse([]string{"-test-axfr", "one.example", "-test-axfr", "two.example"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := make(map[string]int)
	target := tcpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
		name := q.Question[0].Name
		mu.Lock()
		seen[name]++
		mu.Unlock()
		soa := zoneSOA(name)
		w.WriteMsg(axfrMessage(q, soa, soa))
	})
	found := []foundServer{{Address: target.Addr()}}
	reports, err := inspectServers(context.Background(), found, tests.checks(), inspectionOptions{workers: 2, timeout: time.Second, port: target.Port()})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeReports(&output, reports, true); err != nil {
		t.Fatal(err)
	}
	var decoded []serverReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || len(decoded[0].Checks) != 2 {
		t.Fatal(output.String())
	}
	for i, name := range []string{"one.example.", "two.example."} {
		result := decoded[0].Checks[i]
		if result.Subject != name || result.Status != "observed" {
			t.Fatal(result)
		}
		mu.Lock()
		count := seen[name]
		mu.Unlock()
		if count != 1 {
			t.Fatalf("zone %s requested %d times", name, count)
		}
	}
	if len(axfrChecks(nil, 100, 4096)) != 0 {
		t.Fatal("scheduled AXFR without a selected zone")
	}
}

func TestAXFRCancellation(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	target := tcpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) { received <- struct{}{}; <-release })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan checkResult, 1)
	go func() { done <- (axfrCheck{"example.test.", 100, 4096}).Run(ctx, target) }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("no AXFR request")
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != "canceled" {
			t.Fatal(result)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("AXFR did not cancel")
	}
}

func TestInspectionOptionValidation(t *testing.T) {
	for _, opts := range []inspectionOptions{{workers: 0, timeout: time.Second}, {workers: 1025, timeout: time.Second}, {workers: 1, timeout: 0}, {workers: 1, timeout: -time.Second}} {
		if _, err := inspectServers(context.Background(), nil, nil, opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
}

func TestVersionTCPFallback(t *testing.T) {
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
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			r.Truncated = true
		} else {
			r.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "version.bind.", Rrtype: dns.TypeTXT, Class: dns.ClassCHAOS}, Txt: []string{"TCP version"}}}
		}
		w.WriteMsg(r)
	})
	servers := []*dns.Server{{Listener: listener, Handler: handler}, {PacketConn: udp, Handler: handler}}
	for _, server := range servers {
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
	target := listener.Addr().(*net.TCPAddr).AddrPort()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := (versionCheck{}).Run(ctx, target)
	if result.Status != "observed" || len(result.Values) != 1 || result.Values[0] != "TCP version" {
		t.Fatal(result)
	}
}
