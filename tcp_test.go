package main

import (
	"context"
	"encoding/binary"
	"flag"
	"io"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestTCPCheck(t *testing.T) {
	for _, mode := range []string{"answer", "refused", "servfail", "wrong question", "wrong id", "truncated", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			target := tcpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) {
				if q.Question[0] != (dns.Question{Name: ".", Qtype: dns.TypeNS, Qclass: dns.ClassINET}) || q.RecursionDesired {
					t.Error("unexpected query", q)
				}
				if mode == "timeout" {
					return
				}
				r := new(dns.Msg).SetReply(q)
				switch mode {
				case "refused":
					r.Rcode = dns.RcodeRefused
				case "servfail":
					r.Rcode = dns.RcodeServerFailure
				case "wrong question":
					r.Question[0].Name = "wrong.example."
				case "wrong id":
					r.Id++
				case "truncated":
					r.Truncated = true
				}
				w.WriteMsg(r)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			got := (tcpCheck{}).Run(ctx, target)
			want := "observed"
			if mode == "timeout" {
				want = "timeout"
			}
			if mode == "wrong question" || mode == "wrong id" || mode == "truncated" {
				want = "error"
			}
			if got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
			if mode == "refused" && got.Observations[0].Rcode != "REFUSED" {
				t.Fatal(got)
			}
		})
	}
}

func TestTCPFramingFailures(t *testing.T) {
	for _, mode := range []string{"close", "incomplete", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				var size [2]byte
				if _, err = io.ReadFull(conn, size[:]); err != nil {
					return
				}
				io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint16(size[:])))
				if mode == "incomplete" {
					conn.Write([]byte{0, 20, 1})
				}
				if mode == "malformed" {
					conn.Write([]byte{0, 3, 1, 2, 3})
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := (tcpCheck{}).Run(ctx, listener.Addr().(*net.TCPAddr).AddrPort())
			<-done
			if got.Status != "error" {
				t.Fatal(got)
			}
		})
	}
}

func TestTCPRefusedAndCanceled(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	target := listener.Addr().(*net.TCPAddr).AddrPort()
	listener.Close()
	if got := (tcpCheck{}).Run(context.Background(), target); got.Status != "error" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := (tcpCheck{}).Run(ctx, target); got.Status != "canceled" {
		t.Fatal(got)
	}
}

func TestTCPSelection(t *testing.T) {
	var opts testOptions
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	opts.register(fs)
	if err := fs.Parse([]string{"-test-tcp", "-test-version"}); err != nil {
		t.Fatal(err)
	}
	checks := opts.checks()
	if len(checks) != 2 {
		t.Fatal(checks)
	}
	if _, ok := checks[0].(tcpCheck); !ok {
		t.Fatal(checks)
	}
}

func TestTCPCancelPendingRead(t *testing.T) {
	received := make(chan struct{})
	target := tcpFixture(t, func(w dns.ResponseWriter, q *dns.Msg) { close(received) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan checkResult, 1)
	go func() { done <- (tcpCheck{}).Run(ctx, target) }()
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
		t.Fatal("cancellation did not interrupt read")
	}
}
