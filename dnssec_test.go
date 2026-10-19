package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSSECBehavior(t *testing.T) {
	for _, mode := range []string{"validating", "nonvalidating", "servfail only", "no ad", "valid fails", "control fails", "control timeout", "valid timeout", "refused", "no recursion", "authoritative", "wrong cd", "wrong rd", "bogus ad", "wrong question", "unrelated answer"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				calls.Add(1)
				if !q.RecursionDesired || !q.AuthenticatedData || q.IsEdns0() == nil || !q.IsEdns0().Do() || q.Question[0].Qtype != dns.TypeA {
					t.Error("incorrect DNSSEC query", q)
				}
				good := q.Question[0].Name == "good.example."
				if (mode == "control timeout" && q.CheckingDisabled) || (mode == "valid timeout" && good) {
					return
				}
				r := new(dns.Msg).SetReply(q)
				r.RecursionAvailable = true
				r.CheckingDisabled = q.CheckingDisabled
				r.AuthenticatedData = good
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
				if !good && !q.CheckingDisabled {
					r.Answer = nil
					r.Rcode = dns.RcodeServerFailure
					r.SetEdns0(1232, true)
					r.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_EDE{InfoCode: 6, ExtraText: "bogus"}}
				}
				switch mode {
				case "nonvalidating":
					if !good {
						r.Rcode = dns.RcodeSuccess
						r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 1)}}
					}
				case "servfail only", "control fails":
					if q.CheckingDisabled {
						r.Rcode = dns.RcodeServerFailure
						r.Answer = nil
					}
				case "no ad":
					r.AuthenticatedData = false
				case "valid fails":
					if good {
						r.Rcode = dns.RcodeServerFailure
						r.Answer = nil
					}
				case "refused":
					r.Rcode = dns.RcodeRefused
					r.Answer = nil
				case "no recursion":
					r.RecursionAvailable = false
				case "authoritative":
					r.Authoritative = true
				case "wrong cd":
					r.CheckingDisabled = !q.CheckingDisabled
				case "wrong rd":
					r.RecursionDesired = false
				case "bogus ad":
					if !good {
						r.AuthenticatedData = true
					}
				case "wrong question":
					r.Question[0].Name = "wrong.example."
				case "unrelated answer":
					if good {
						r.Answer[0].Header().Name = "other.example."
					}
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			got := (dnssecCheck{"good.example.", "bad.example."}).Run(ctx, target)
			want := "inconclusive"
			switch mode {
			case "validating":
				want = "observed"
			case "nonvalidating":
				want = "not-observed"
			case "refused":
				want = "refused"
			case "wrong cd", "wrong rd", "wrong question":
				want = "error"
			case "control timeout", "valid timeout":
				want = "timeout"
			}
			if got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
			if calls.Load() != 3 {
				t.Fatal("missing probes", calls.Load())
			}
			if mode == "validating" {
				if len(got.Observations) != 3 || len(got.Observations[1].ExtendedErrors) != 1 || got.Observations[1].ExtendedErrors[0].Code != 6 {
					t.Fatal(got)
				}
				if got.Observations[0].Authenticated == nil || !*got.Observations[0].Authenticated || !*got.Observations[2].CheckingDisabled {
					t.Fatal(got)
				}
				var out bytes.Buffer
				if err := writeReports(&out, []serverReport{{Address: target.Addr(), Checks: []checkResult{got}}}, false); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "AD=true") || !strings.Contains(out.String(), "EDE 6") {
					t.Fatal(out.String())
				}
				out.Reset()
				if err := writeReports(&out, []serverReport{{Address: target.Addr(), Checks: []checkResult{got}}}, true); err != nil {
					t.Fatal(err)
				}
				var reports []serverReport
				if err := json.Unmarshal(out.Bytes(), &reports); err != nil || reports[0].Checks[0].Observations[1].ExtendedErrors[0].Code != 6 {
					t.Fatal(err, out.String())
				}
			}
		})
	}
}

func TestDNSSECCancellation(t *testing.T) {
	received := make(chan struct{}, 3)
	target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) { received <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan checkResult, 1)
	go func() { done <- (dnssecCheck{"good.example.", "bad.example."}).Run(ctx, target) }()
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

func TestDNSSECOptions(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		valid bool
	}{
		{[]string{"-test-dnssec", "-dnssec-valid-name", "good.example", "-dnssec-bogus-name", "bad.example", "-test-version", "-test-tcp"}, true},
		{[]string{"-test-dnssec", "-dnssec-valid-name", "same.example", "-dnssec-bogus-name", "SAME.example."}, false},
		{[]string{"-test-dnssec", "-dnssec-valid-name", "bad..example"}, false},
	} {
		var opts testOptions
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		opts.register(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if err := opts.validate(); (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
		if tc.valid {
			checks := opts.checks()
			if len(checks) != 3 {
				t.Fatal(checks)
			}
			check := checks[0].(dnssecCheck)
			if check.valid != "good.example." || check.bogus != "bad.example." {
				t.Fatal(check)
			}
		}
	}
}
