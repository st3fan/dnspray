package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"
)

func TestMultipleASNsWithEmptyResultReachWire(t *testing.T) {
	var servers []*net.UDPConn
	var counts [3]atomic.Int32
	var wg sync.WaitGroup
	var port uint16
	for i, host := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr(host), port)))
		if err != nil {
			for _, server := range servers {
				server.Close()
			}
			t.Fatal(err)
		}
		port = conn.LocalAddr().(*net.UDPAddr).AddrPort().Port()
		servers = append(servers, conn)
		wg.Add(1)
		go func() {
			defer wg.Done()
			packet := make([]byte, 4096)
			for {
				n, peer, err := conn.ReadFromUDPAddrPort(packet)
				if err != nil {
					return
				}
				counts[i].Add(1)
				q := new(dns.Msg)
				if q.Unpack(packet[:n]) != nil {
					continue
				}
				r := new(dns.Msg).SetReply(q)
				wire, _ := r.Pack()
				conn.WriteToUDPAddrPort(wire, peer)
			}
		}()
	}
	defer func() {
		for _, conn := range servers {
			conn.Close()
		}
		wg.Wait()
	}()
	client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		asn := r.URL.Query().Get("resource")
		if asn == "AS64499" {
			w.Write(ripeFixtureBody(asn))
			return
		}
		host := map[string]string{"AS64496": "127.0.0.1/32", "AS64497": "127.0.0.2/32", "AS64498": "127.0.0.3/32"}[asn]
		w.Write(ripeFixtureBody(asn, host))
	})
	service := cliServices{
		lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
			client.logger = logger
			return client.prefixes(ctx, asn)
		},
		discover: func(ctx context.Context, prefixes []netip.Prefix, q *dns.Msg, opts scanOptions, out io.Writer) ([]foundServer, error) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			s := scanner{opts: opts, sockets: map[bool]*net.UDPConn{false: conn}, question: q, found: make(map[netip.Addr]bool), output: out, port: port}
			err = s.run(ctx, prefixes, opts.timeout)
			return s.servers, err
		},
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), []string{"-json", "-t", "0.05", "-retry-gap", "1ms", "AS64496", "AS64499", "AS64497", "AS64498"}, &stdout, &stderr, service)
	var reports []serverReport
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil || code != 0 || len(reports) != 3 {
		t.Fatalf("code=%d reports=%v error=%v stderr=%s", code, reports, err, stderr.String())
	}
	for i := range counts {
		if counts[i].Load() != 3 {
			t.Fatalf("ASN responder %d got %d probes", i, counts[i].Load())
		}
	}
	t.Log("All three nonempty ASN targets received all three probes and appeared in output")
}
