package main

import (
	"context"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestAuthorityEvidence(t *testing.T) {
	for _, mode := range []string{"authoritative", "negative", "cached SOA", "referral", "refused", "unsupported", "servfail", "wrong question", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.RecursionDesired || q.Question[0].Qtype != dns.TypeSOA || q.Question[0].Name != "child.example." {
					t.Errorf("invalid authority query: %v", q)
				}
				if mode == "timeout" {
					return
				}
				r := new(dns.Msg).SetReply(q)
				soa := &dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET}, Ns: "ns.example.", Mbox: "hostmaster.example.", Serial: 1}
				switch mode {
				case "authoritative":
					r.Authoritative = true
					soa.Hdr.Name = q.Question[0].Name
					r.Answer = []dns.RR{soa}
				case "negative":
					r.Authoritative = true
					r.Rcode = dns.RcodeNameError
					r.Ns = []dns.RR{soa}
				case "cached SOA":
					r.Answer = []dns.RR{soa}
				case "referral":
					r.Ns = []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET}, Ns: "ns.example."}}
				case "refused":
					r.Rcode = dns.RcodeRefused
				case "unsupported":
					r.Rcode = dns.RcodeNotImplemented
				case "servfail":
					r.Authoritative = true
					r.Rcode = dns.RcodeServerFailure
				case "wrong question":
					r.Question = []dns.Question{{Name: "wrong.example.", Qtype: dns.TypeSOA, Qclass: dns.ClassINET}}
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result := (authorityCheck{name: "child.example."}).Run(ctx, target)
			want := map[string]string{"authoritative": "observed", "negative": "observed", "cached SOA": "not-authoritative", "referral": "not-authoritative", "refused": "refused", "unsupported": "unsupported", "servfail": "inconclusive", "wrong question": "error", "timeout": "timeout"}[mode]
			if result.Status != want {
				t.Fatalf("got %+v want %s", result, want)
			}
			if mode == "negative" && (len(result.Values) != 1 || !result.Observations[0].Authoritative) {
				t.Fatal(result)
			}
		})
	}
}

func TestExplicitTestSelection(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		checks   []string
		subjects []string
	}{
		{nil, nil, nil},
		{[]string{"-test-version"}, []string{"version"}, []string{"version.bind."}},
		{[]string{"-test-authority", "one.example", "-test-authority", "two.example", "-test-recursion", "probe.example"}, []string{"authority", "authority", "recursion"}, []string{"one.example.", "two.example.", "probe.example."}},
		{[]string{"-test-version=false"}, nil, nil},
	} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var opts testOptions
		opts.register(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		checks := opts.checks()
		if len(checks) != len(tc.checks) {
			t.Fatalf("%v selected %d checks", tc.args, len(checks))
		}
		target := udpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
			r := new(dns.Msg).SetReply(q)
			r.Rcode = dns.RcodeRefused
			w.WriteMsg(r)
		})
		for i, check := range checks {
			result := check.Run(context.Background(), target)
			if result.Check != tc.checks[i] || result.Subject != tc.subjects[i] {
				t.Fatal(result)
			}
		}
	}
	fs := flag.NewFlagSet("invalid", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts testOptions
	opts.register(fs)
	if fs.Parse([]string{"-test-authority", "bad..name"}) == nil {
		t.Fatal("accepted invalid authority name")
	}
}
