package main

import (
	"context"
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

	"github.com/miekg/dns"
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

// makeQuestion validates the hostname and builds an IN A query.
func makeQuestion(host string) (*dns.Msg, error) {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return nil, errors.New("hostname must be between 1 and 253 ASCII characters")
	}

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
	}
	return new(dns.Msg).SetQuestion(dns.Fqdn(host), dns.TypeA), nil
}

func scan(ctx context.Context, prefixes []netip.Prefix, question *dns.Msg, concurrency int) {
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
func probe(ctx context.Context, addr netip.Addr, port string, question *dns.Msg) bool {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	client := &dns.Client{Net: "udp", Timeout: queryTimeout, UDPSize: 4096}
	conn, err := client.DialContext(ctx, net.JoinHostPort(addr.String(), port))
	if err != nil {
		return false
	}
	defer conn.Close()
	// ExchangeWithConnContext applies deadlines but does not interrupt reads
	// when the context is canceled before its deadline.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	query := question.Copy()
	query.Id = dns.Id()
	response, _, err := client.ExchangeWithConnContext(ctx, query, conn)
	return err == nil && validResponse(query, response)
}

func validResponse(query, response *dns.Msg) bool {
	if query == nil || response == nil || len(query.Question) != 1 || len(response.Question) != 1 {
		return false
	}
	return response.Id == query.Id && response.Response &&
		response.Opcode == dns.OpcodeQuery &&
		response.Question[0] == query.Question[0]
}
