package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

const queryTimeout = 2 * time.Second

func main() {
	host := flag.String("h", "www.example.com", "hostname to query")
	concurrency := flag.Int("c", 32, "maximum number of concurrent queries")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [-h hostname] [-c N] CIDR-or-IP [CIDR-or-IP ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *concurrency < 1 {
		fmt.Fprintln(os.Stderr, "-c must be at least 1")
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	question, err := makeQuestion(*host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	contained, err := parsePrefixes(flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	scan(ctx, contained, question, *concurrency)
}

func parsePrefixes(args []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(args))
	for _, arg := range args {
		p, err := netip.ParsePrefix(arg)
		if err != nil {
			addr, addrErr := netip.ParseAddr(arg)
			if addrErr != nil {
				return nil, fmt.Errorf("invalid CIDR or IP %q: %w", arg, err)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		prefixes = append(prefixes, p.Masked())
	}
	return prefixes, nil
}

// makeQuestion builds the fixed part of an IN A query. The two-byte ID is
// filled in separately for each request.
func makeQuestion(host string) ([]byte, error) {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return nil, errors.New("hostname must be between 1 and 253 ASCII characters")
	}

	q := make([]byte, 12, 12+len(host)+6)
	q[2] = 0x01 // Recursion desired.
	q[5] = 0x01 // One question.
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS label in hostname %q", host)
		}
		for _, ch := range []byte(label) {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
				return nil, fmt.Errorf("invalid character in hostname %q", host)
			}
		}
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // Root label, type A, class IN.
	return q, nil
}

func scan(ctx context.Context, prefixes []netip.Prefix, question []byte, concurrency int) {
	jobs := make(chan netip.Addr, concurrency)
	results := make(chan netip.Addr, concurrency)
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for addr := range jobs {
				if ctx.Err() != nil {
					return
				}
				if probe(ctx, addr, "53", question) {
					results <- addr
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, prefix := range prefixes {
			for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
				select {
				case jobs <- addr:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	for addr := range results {
		fmt.Println(addr)
	}
}

// probe reports a DNS server when it returns a response for our exact question.
// Any DNS response code counts, including NXDOMAIN and REFUSED.
func probe(ctx context.Context, addr netip.Addr, port string, question []byte) bool {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(addr.String(), port))
	if err != nil {
		return false
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return false
	}

	query := bytes.Clone(question)
	if _, err := rand.Read(query[:2]); err != nil {
		return false
	}
	if _, err := conn.Write(query); err != nil {
		return false
	}

	response := make([]byte, 4096)
	n, err := conn.Read(response)
	if err != nil || n < len(query) {
		return false
	}
	return validResponse(query, response[:n])
}

func validResponse(query, response []byte) bool {
	if len(query) < 12 || len(response) < len(query) {
		return false
	}
	return binary.BigEndian.Uint16(response[:2]) == binary.BigEndian.Uint16(query[:2]) &&
		response[2]&0x80 != 0 && // Response bit.
		response[2]&0x78 == 0 && // Standard query opcode.
		binary.BigEndian.Uint16(response[4:6]) == 1 &&
		bytes.Equal(response[12:len(query)], query[12:])
}
