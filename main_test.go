package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestProbe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*dns.Msg)
		malformed bool
		want      bool
	}{
		{name: "answer", want: true},
		{name: "NXDOMAIN", change: func(m *dns.Msg) { m.Rcode = dns.RcodeNameError }, want: true},
		{name: "REFUSED", change: func(m *dns.Msg) { m.Rcode = dns.RcodeRefused }, want: true},
		{name: "wrong transaction ID", change: func(m *dns.Msg) { m.Id ^= 1 }},
		{name: "wrong question name", change: func(m *dns.Msg) { m.Question[0].Name = "other.example." }},
		{name: "wrong question type", change: func(m *dns.Msg) { m.Question[0].Qtype = dns.TypeAAAA }},
		{name: "wrong question class", change: func(m *dns.Msg) { m.Question[0].Qclass = dns.ClassCHAOS }},
		{name: "missing question", change: func(m *dns.Msg) { m.Question = nil }},
		{name: "extra question", change: func(m *dns.Msg) { m.Question = append(m.Question, m.Question[0]) }},
		{name: "wrong opcode", change: func(m *dns.Msg) { m.Opcode = dns.OpcodeStatus }},
		{name: "not a response", change: func(m *dns.Msg) { m.Response = false }},
		{name: "malformed packet", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, port, _ := startUDPServer(t, func(conn *net.UDPConn, peer *net.UDPAddr, packet []byte) {
				query := new(dns.Msg)
				if err := query.Unpack(packet); err != nil {
					t.Error(err)
					return
				}
				response := new(dns.Msg).SetReply(query)
				if tc.change != nil {
					tc.change(response)
				}
				wire, err := response.Pack()
				if err != nil {
					t.Error(err)
					return
				}
				if tc.malformed {
					wire = wire[:11]
				}
				if _, err := conn.WriteToUDP(wire, peer); err != nil {
					t.Error(err)
				}
			})
			query, err := makeQuestion("www.example.com")
			if err != nil {
				t.Fatal(err)
			}
			original := query.Copy()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if got := probe(ctx, addr, port, query); got != tc.want {
				t.Fatalf("probe() = %v, want %v", got, tc.want)
			}
			if query.String() != original.String() {
				t.Fatal("probe modified shared query")
			}
		})
	}
}

func TestProbeTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"query timeout", "context deadline", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			addr, port, received := startUDPServer(t, func(*net.UDPConn, *net.UDPAddr, []byte) {})
			ctx := context.Background()
			var cancel context.CancelFunc
			if mode == "context deadline" {
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			query, err := makeQuestion("www.example.com")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			started := time.Now()
			go func() { done <- probe(ctx, addr, port, query) }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("server did not receive query")
			}
			limit := time.Second
			if mode == "cancellation" {
				cancel()
			}
			if mode == "query timeout" {
				limit = queryTimeout + time.Second
			}
			select {
			case got := <-done:
				if got {
					t.Fatal("silent server counted as DNS server")
				}
				if mode == "query timeout" && time.Since(started) < queryTimeout-100*time.Millisecond {
					t.Fatal("probe returned before query timeout")
				}
			case <-time.After(limit):
				t.Fatal("probe did not stop in time")
			}
		})
	}
}

// startUDPServer uses an ephemeral loopback port; tests never scan external hosts.
func startUDPServer(t *testing.T, handle func(*net.UDPConn, *net.UDPAddr, []byte)) (netip.Addr, string, <-chan struct{}) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		packet := make([]byte, 4096)
		n, peer, err := conn.ReadFromUDP(packet)
		if err != nil {
			return
		}
		close(received)
		handle(conn, peer, packet[:n])
	}()
	t.Cleanup(func() { conn.Close(); <-done })
	host, port, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return netip.MustParseAddr(host), port, received
}

func TestQuestion(t *testing.T) {
	for _, host := range []string{"example.com", "example.com.", "_service.example", strings.Repeat("a", 63) + ".example"} {
		q, err := makeQuestion(host)
		if err != nil {
			t.Fatalf("makeQuestion(%q): %v", host, err)
		}
		if len(q.Question) != 1 || q.Question[0].Name != dns.Fqdn(host) ||
			q.Question[0].Qtype != dns.TypeA || q.Question[0].Qclass != dns.ClassINET || !q.RecursionDesired {
			t.Fatalf("unexpected question: %v", q)
		}
		if _, err := q.Pack(); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"", ".", "bad..name", "bad name", "é.example", strings.Repeat("a", 64) + ".example", strings.Repeat("a", 254)} {
		if _, err := makeQuestion(host); err == nil {
			t.Errorf("accepted invalid hostname %q", host)
		}
	}
}

func TestInputs(t *testing.T) {
	prefixes, err := parsePrefixes([]string{"192.0.2.123/24", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixes[0].String(); got != "192.0.2.0/24" {
		t.Fatalf("masked prefix = %q", got)
	}
	if got := prefixes[1].String(); got != "2001:db8::1/128" {
		t.Fatalf("single address = %q", got)
	}
	if _, err := makeQuestion("bad..name"); err == nil {
		t.Fatal("expected invalid hostname to fail")
	}
}
