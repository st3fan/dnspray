package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func main() {
	host := flag.String("h", "", "optional non-recursive A discovery query (default: root NS)")
	timeout := flag.Float64("t", 2, "seconds to listen after the final request")
	rate := flag.Int("rate", defaultRate, "maximum packets per second (0 disables pacing)")
	gap := flag.Duration("retry-gap", defaultRetryGap, "minimum delay between attempts to the same address")
	discoverOnly := flag.Bool("discover-only", false, "only discover responding DNS addresses")
	jsonOutput := flag.Bool("json", false, "write structured server reports as JSON")
	checkWorkers := flag.Int("check-workers", 8, "maximum concurrent server inspections")
	checkTimeout := flag.Duration("check-timeout", 2*time.Second, "deadline for each capability check")
	var tests testOptions
	tests.register(flag.CommandLine)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options] CIDR-or-IP [CIDR-or-IP ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if math.IsNaN(*timeout) || math.IsInf(*timeout, 0) || *timeout <= 0 || *timeout >= float64(math.MaxInt64)/float64(time.Second) || time.Duration(*timeout*float64(time.Second)) <= 0 {
		fmt.Fprintln(os.Stderr, "-t must be a positive, representable number of seconds")
		os.Exit(2)
	}
	opts := scanOptions{timeout: time.Duration(*timeout * float64(time.Second)), rate: *rate, retryGap: *gap}
	if err := opts.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	inspection := inspectionOptions{workers: *checkWorkers, timeout: *checkTimeout, port: 53}
	if err := inspection.validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *discoverOnly && *jsonOutput {
		fmt.Fprintln(os.Stderr, "-json cannot be combined with -discover-only")
		os.Exit(2)
	}
	checks := tests.checks()
	if *discoverOnly && len(checks) > 0 {
		fmt.Fprintln(os.Stderr, "-discover-only cannot be combined with -test-* options")
		os.Exit(2)
	}
	onlyAddresses := *discoverOnly || (len(checks) == 0 && !*jsonOutput)
	question, err := discoveryQuestion(*host)
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
	output := io.Writer(io.Discard)
	if onlyAddresses {
		output = os.Stdout
	}
	found, err := discover(ctx, contained, question, opts, output)
	if err == nil && !onlyAddresses {
		var reports []serverReport
		reports, err = inspectServers(ctx, found, checks, inspection)
		if err == nil {
			err = writeReports(os.Stdout, reports, *jsonOutput)
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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

func validResponse(query, response *dns.Msg) bool {
	if query == nil || response == nil || len(query.Question) != 1 || len(response.Question) != 1 {
		return false
	}
	return response.Id == query.Id && response.Response &&
		response.Opcode == dns.OpcodeQuery &&
		response.Question[0] == query.Question[0]
}

// Discovery requests no recursive work; any matching DNS response is sufficient.
func discoveryQuestion(host string) (*dns.Msg, error) {
	if host == "" {
		q := new(dns.Msg).SetQuestion(".", dns.TypeNS)
		q.RecursionDesired = false
		return q, nil
	}
	q, err := makeQuestion(host)
	if err != nil {
		return nil, err
	}
	q.RecursionDesired = false
	return q, nil
}
