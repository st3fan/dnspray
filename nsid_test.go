package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestNSIDResponses(t *testing.T) {
	for _, tc := range []struct {
		mode, status string
		hex          *string
	}{
		{"printable", "observed", stringValue("6e732d31")},
		{"binary", "observed", stringValue("00ff0a1b")},
		{"empty", "observed", stringValue("")},
		{"absent", "inconclusive", nil},
		{"no edns", "inconclusive", nil},
		{"formerr", "edns-rejected", nil},
		{"badvers", "edns-rejected", nil},
		{"refused", "refused", nil},
		{"duplicate", "error", nil},
		{"multiple opt", "error", nil},
		{"wrong question", "error", nil},
		{"malformed", "error", nil},
		{"timeout", "timeout", nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				opt := q.IsEdns0()
				if opt == nil || opt.Version() != 0 || len(opt.Option) != 1 || q.RecursionDesired || q.Question[0].Name != "." || q.Question[0].Qtype != dns.TypeNS {
					t.Error("invalid NSID query", q)
					return
				}
				req, ok := opt.Option[0].(*dns.EDNS0_NSID)
				if !ok || req.Nsid != "" {
					t.Error("NSID request must be empty", req)
				}
				if tc.mode == "timeout" {
					return
				}
				r := new(dns.Msg).SetReply(q)
				if tc.mode != "no edns" {
					r.SetEdns0(1232, false)
				}
				switch tc.mode {
				case "printable", "binary", "empty":
					r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_NSID{Code: dns.EDNS0NSID, Nsid: *tc.hex}}
				case "formerr":
					r.Rcode = dns.RcodeFormatError
				case "badvers":
					r.Rcode = dns.RcodeBadVers
					r.IsEdns0().SetVersion(1)
				case "refused":
					r.Rcode = dns.RcodeRefused
				case "duplicate":
					r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_NSID{Code: dns.EDNS0NSID}, &dns.EDNS0_NSID{Code: dns.EDNS0NSID}}
				case "multiple opt":
					r.Extra = append(r.Extra, r.IsEdns0())
				case "wrong question":
					r.Question[0].Name = "wrong.example."
				case "malformed":
					r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_NSID{Code: dns.EDNS0NSID, Nsid: "6162"}}
					wire, err := r.Pack()
					if err != nil {
						t.Error(err)
						return
					}
					w.Write(wire[:len(wire)-1])
					return
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			got := (nsidCheck{}).Run(ctx, target)
			if got.Status != tc.status || !reflect.DeepEqual(got.NSIDHex, tc.hex) {
				t.Fatalf("%+v want %s %v", got, tc.status, tc.hex)
			}
			if tc.hex != nil {
				if got.Observations[0].EDNS == nil || got.Observations[0].EDNS.UDPSize != 1232 {
					t.Fatal(got)
				}
				var jsonOut, text bytes.Buffer
				reports := []serverReport{{Address: target.Addr(), Checks: []checkResult{got}}}
				if err := writeReports(&jsonOut, reports, true); err != nil {
					t.Fatal(err)
				}
				var decoded []serverReport
				if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded[0].Checks[0].NSIDHex == nil || *decoded[0].Checks[0].NSIDHex != *tc.hex {
					t.Fatal(jsonOut.String())
				}
				if err := writeReports(&text, reports, false); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(text.String(), "NSID hex: \""+*tc.hex+"\"") {
					t.Fatal(text.String())
				}
			} else {
				wire, _ := json.Marshal(got)
				if strings.Contains(string(wire), "nsid_hex") {
					t.Fatal("absent identifier serialized", string(wire))
				}
			}
		})
	}
}

func stringValue(value string) *string { return &value }

func TestNSIDCancellation(t *testing.T) {
	received := make(chan struct{}, 1)
	target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) { received <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan checkResult, 1)
	go func() { done <- (nsidCheck{}).Run(ctx, target) }()
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
	case <-time.After(time.Second):
		t.Fatal("cancellation failed")
	}
}

func TestCapabilityCLISelection(t *testing.T) {
	var opts testOptions
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	opts.register(fs)
	args := []string{"-test-nsid", "-test-dnssec", "-test-tcp", "-test-version"}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	checks := opts.checks()
	if len(checks) != 4 {
		t.Fatal(checks)
	}
	if _, ok := checks[0].(nsidCheck); !ok {
		t.Fatal(checks)
	}
	for _, silent := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		cliArgs := append([]string{"-json"}, args...)
		if silent {
			cliArgs = append([]string{"-silent"}, cliArgs...)
		}
		cliArgs = append(cliArgs, "192.0.2.1")
		code := runCLI(context.Background(), cliArgs, &stdout, &stderr, cliServices{
			discover: func(context.Context, []netip.Prefix, *dns.Msg, scanOptions, io.Writer) ([]foundServer, error) {
				return nil, nil
			},
		})
		if code != 0 || strings.TrimSpace(stdout.String()) != "[]" {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		if silent && stderr.Len() != 0 {
			t.Fatal(stderr.String())
		}
	}
	for _, option := range []string{"-test-tcp", "-test-dnssec", "-test-nsid"} {
		var stdout, stderr bytes.Buffer
		code := runCLI(context.Background(), []string{"-discover-only", option, "192.0.2.1"}, &stdout, &stderr, cliServices{})
		if code != 2 {
			t.Fatal(option, code, stderr.String())
		}
	}
}

func TestCombinedCapabilityInspection(t *testing.T) {
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		r := new(dns.Msg).SetReply(q)
		r.CheckingDisabled = q.CheckingDisabled
		r.RecursionAvailable = q.RecursionDesired
		if q.Question[0].Qtype == dns.TypeA {
			good := q.Question[0].Name == "good.example."
			r.AuthenticatedData = good
			if good || q.CheckingDisabled {
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
			} else {
				r.Rcode = dns.RcodeServerFailure
			}
		} else if q.IsEdns0() != nil {
			r.SetEdns0(1232, false)
			r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_NSID{Code: dns.EDNS0NSID, Nsid: "00ff"}}
		}
		w.WriteMsg(r)
	})
	target := tcpFixture(t, handler)
	conn, err := net.ListenPacket("udp4", target.String())
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: handler}
	ready := make(chan struct{})
	server.NotifyStartedFunc = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- server.ActivateAndServe() }()
	<-ready
	defer func() {
		server.Shutdown()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	reports, err := inspectServers(context.Background(), []foundServer{{Address: target.Addr()}}, []serverCheck{tcpCheck{}, dnssecCheck{"good.example.", "bad.example."}, nsidCheck{}}, inspectionOptions{workers: 1, timeout: time.Second, port: target.Port()})
	if err != nil || len(reports) != 1 || len(reports[0].Checks) != 3 {
		t.Fatal(reports, err)
	}
	for _, check := range reports[0].Checks {
		if check.Status != "observed" {
			t.Fatal(check)
		}
	}
}
