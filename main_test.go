package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const retryGap = 50 * time.Millisecond

func TestValidResponse(t *testing.T) {
	query, _ := makeQuestion("www.example.com")
	for _, tc := range []struct {
		name   string
		change func(*dns.Msg)
		want   bool
	}{
		{"answer", func(m *dns.Msg) {}, true},
		{"NXDOMAIN", func(m *dns.Msg) { m.Rcode = dns.RcodeNameError }, true},
		{"REFUSED", func(m *dns.Msg) { m.Rcode = dns.RcodeRefused }, true},
		{"wrong ID", func(m *dns.Msg) { m.Id ^= 1 }, false},
		{"wrong name", func(m *dns.Msg) { m.Question[0].Name = "other.example." }, false},
		{"wrong type", func(m *dns.Msg) { m.Question[0].Qtype = dns.TypeAAAA }, false},
		{"wrong class", func(m *dns.Msg) { m.Question[0].Qclass = dns.ClassCHAOS }, false},
		{"missing question", func(m *dns.Msg) { m.Question = nil }, false},
		{"extra question", func(m *dns.Msg) { m.Question = append(m.Question, m.Question[0]) }, false},
		{"wrong opcode", func(m *dns.Msg) { m.Opcode = dns.OpcodeStatus }, false},
		{"not response", func(m *dns.Msg) { m.Response = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := new(dns.Msg).SetReply(query)
			tc.change(response)
			if got := validResponse(query, response); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// All integration traffic stays on ephemeral loopback ports.
func testScanner(t *testing.T, v6 bool, output io.Writer) (*scanner, *net.UDPConn, netip.Prefix) {
	t.Helper()
	network, host := "udp4", "127.0.0.1"
	if v6 {
		network, host = "udp6", "::1"
	}
	server, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP(host)})
	if err != nil {
		if v6 {
			t.Skipf("IPv6 unavailable: %v", err)
		}
		t.Fatal(err)
	}
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP(host)})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(); conn.Close() })
	question, _ := makeQuestion("www.example.com")
	addr := netip.MustParseAddr(host)
	return &scanner{opts: scanOptions{retryGap: retryGap}, sockets: map[bool]*net.UDPConn{v6: conn}, question: question, found: make(map[netip.Addr]bool), output: output, port: server.LocalAddr().(*net.UDPAddr).AddrPort().Port()}, server, netip.PrefixFrom(addr, addr.BitLen())
}

type timedWriter struct {
	bytes.Buffer
	times []time.Time
}

func (w *timedWriter) Write(p []byte) (int, error) {
	w.times = append(w.times, time.Now())
	return w.Buffer.Write(p)
}

func TestScanRetriesAndConcurrentReplies(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		t.Run(map[bool]string{false: "IPv4", true: "IPv6"}[v6], func(t *testing.T) {
			output := new(timedWriter)
			s, server, prefix := testScanner(t, v6, output)
			received := make(chan time.Time, 3)
			done := make(chan struct{})
			go func() {
				defer close(done)
				packet := make([]byte, 4096)
				for range 3 {
					n, peer, err := server.ReadFromUDPAddrPort(packet)
					if err != nil {
						return
					}
					received <- time.Now()
					q := new(dns.Msg)
					if err := q.Unpack(packet[:n]); err != nil {
						t.Error(err)
						return
					}
					// Malformed and mismatched traffic must not stop the listener.
					server.WriteToUDPAddrPort([]byte{1, 2, 3}, peer)
					wrong := new(dns.Msg).SetReply(q)
					wrong.Id ^= 1
					wire, _ := wrong.Pack()
					server.WriteToUDPAddrPort(wire, peer)
					response := new(dns.Msg).SetReply(q)
					response.Rcode = dns.RcodeRefused
					wire, _ = response.Pack()
					for range 2 {
						server.WriteToUDPAddrPort(wire, peer)
					}
				}
			}()
			t.Cleanup(func() { server.Close(); <-done })
			started := time.Now()
			if err := s.run(context.Background(), []netip.Prefix{prefix}, 80*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			times := []time.Time{<-received, <-received, <-received}
			for i := 1; i < 3; i++ {
				if times[i].Sub(times[i-1]) < retryGap-5*time.Millisecond {
					t.Fatal("retries too close")
				}
			}
			if time.Since(started) < 2*retryGap+80*time.Millisecond {
				t.Fatal("grace period started before final send")
			}
			if output.String() != prefix.Addr().String()+"\n" {
				t.Fatalf("unexpected results: %q", output.String())
			}
			if len(s.servers) != 1 || s.servers[0].Address != prefix.Addr() {
				t.Fatalf("discovery list not deduplicated: %v", s.servers)
			}
			if len(output.times) != 1 || !output.times[0].Before(times[2]) {
				t.Fatal("reply not processed during sending")
			}
		})
	}
}

func TestDiscoveryQuestion(t *testing.T) {
	for _, host := range []string{"", "example.com"} {
		q, err := discoveryQuestion(host)
		if err != nil || q.RecursionDesired {
			t.Fatalf("discovery must not request recursion: %v %v", q, err)
		}
		if host == "" && (q.Question[0].Name != "." || q.Question[0].Qtype != dns.TypeNS) {
			t.Fatalf("unexpected default discovery question: %v", q)
		}
	}
}

func TestScanLateReply(t *testing.T) {
	output := new(bytes.Buffer)
	s, server, prefix := testScanner(t, false, output)
	done := make(chan struct{})
	go func() {
		defer close(done)
		packet := make([]byte, 4096)
		for i := 0; i < 3; i++ {
			n, peer, err := server.ReadFromUDPAddrPort(packet)
			if err != nil {
				return
			}
			if i == 2 {
				time.Sleep(40 * time.Millisecond)
				q := new(dns.Msg)
				q.Unpack(packet[:n])
				wire, _ := new(dns.Msg).SetReply(q).Pack()
				server.WriteToUDPAddrPort(wire, peer)
			}
		}
	}()
	t.Cleanup(func() { server.Close(); <-done })
	if err := s.run(context.Background(), []netip.Prefix{prefix}, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("missed reply after final send")
	}
}

func TestScanCancellationAndErrors(t *testing.T) {
	for _, mode := range []string{"cancel", "read error", "send error"} {
		t.Run(mode, func(t *testing.T) {
			s, server, prefix := testScanner(t, false, io.Discard)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "send error" {
				s.sockets[false].Close()
			}
			done := make(chan error, 1)
			go func() { done <- s.run(ctx, []netip.Prefix{prefix}, time.Second) }()
			if mode != "send error" {
				server.SetReadDeadline(time.Now().Add(time.Second))
				if _, _, err := server.ReadFromUDPAddrPort(make([]byte, 4096)); err != nil {
					t.Fatal(err)
				}
				if mode == "cancel" {
					cancel()
				} else {
					s.sockets[false].Close()
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected error")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("scan failed to stop")
			}
		})
	}
}

func TestReplySourceValidation(t *testing.T) {
	output := new(bytes.Buffer)
	s, _, prefix := testScanner(t, false, output)
	addr := prefix.Addr()
	s.sent = []frontier{{prefix: prefix}}
	q := s.question.Copy()
	q.Id = s.id(addr)
	response := new(dns.Msg).SetReply(q)
	peer := netip.AddrPortFrom(addr, s.port)
	s.accept(peer, response)
	if output.Len() != 0 {
		t.Fatal("accepted unsent target")
	}
	s.sent[0].last = addr
	s.accept(netip.AddrPortFrom(addr, s.port+1), response)
	other := netip.MustParseAddr("127.0.0.2")
	q.Id = s.id(other)
	s.accept(netip.AddrPortFrom(other, s.port), new(dns.Msg).SetReply(q))
	if output.Len() != 0 {
		t.Fatal("accepted wrong source")
	}
	s.accept(peer, response)
	if output.Len() == 0 {
		t.Fatal("rejected matching reply")
	}
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResultWriteError(t *testing.T) {
	s, _, prefix := testScanner(t, false, failingWriter{})
	s.sent = []frontier{{prefix: prefix, last: prefix.Addr()}}
	q := s.question.Copy()
	q.Id = s.id(prefix.Addr())
	if err := s.accept(netip.AddrPortFrom(prefix.Addr(), s.port), new(dns.Msg).SetReply(q)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v", err)
	}
}

func TestPacingAcrossTargetsAndRetries(t *testing.T) {
	s, server, prefix := testScanner(t, false, io.Discard)
	s.opts.rate = 40 // 25ms between every packet, across both prefix entries.
	s.opts.retryGap = time.Millisecond
	received := make(chan time.Time, 6)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 6 {
			if _, _, err := server.ReadFromUDPAddrPort(make([]byte, 4096)); err != nil {
				return
			}
			received <- time.Now()
		}
	}()
	t.Cleanup(func() { server.Close(); <-done })
	if err := s.run(context.Background(), []netip.Prefix{prefix, prefix}, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for range 6 {
		select {
		case now := <-received:
			if !previous.IsZero() && now.Sub(previous) < 20*time.Millisecond {
				t.Fatal("packets exceeded configured rate")
			}
			previous = now
		default:
			t.Fatal("did not receive all six attempts")
		}
	}
}

func TestCancellationWhilePacing(t *testing.T) {
	s, server, prefix := testScanner(t, false, io.Discard)
	s.opts.rate = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx, []netip.Prefix{prefix}, time.Second) }()
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := server.ReadFromUDPAddrPort(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("pacing did not cancel promptly")
	}
}

func TestScanOptions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rate         int
		gap, timeout time.Duration
		valid        bool
	}{
		{"defaults", defaultRate, defaultRetryGap, 2 * time.Second, true},
		{"unpaced", 0, time.Second, 5 * time.Second, true},
		{"negative rate", -1, time.Second, time.Second, false},
		{"unrepresentable interval", int(time.Second) + 1, time.Second, time.Second, false},
		{"zero retry gap", 500, 0, time.Second, false},
		{"negative retry gap", 500, -time.Second, time.Second, false},
		{"zero timeout", 500, time.Second, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (scanOptions{rate: tc.rate, retryGap: tc.gap, timeout: tc.timeout}).validate()
			if (err == nil) != tc.valid {
				t.Fatalf("validation error %v, valid=%v", err, tc.valid)
			}
		})
	}
}
