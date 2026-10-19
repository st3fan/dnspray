package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type cliServices struct {
	lookup   func(context.Context, string, *log.Logger) ([]netip.Prefix, error)
	discover func(context.Context, []netip.Prefix, *dns.Msg, scanOptions, io.Writer) ([]foundServer, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr, cliServices{})
	stop()
	os.Exit(code)
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, services cliServices) int {
	flags := flag.NewFlagSet("find-nameservers", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("h", "", "optional non-recursive A discovery query (default: root NS)")
	timeout := flags.Float64("t", 2, "seconds to listen after the final request")
	rate := flags.Int("rate", defaultRate, "maximum packets per second (0 disables pacing)")
	gap := flags.Duration("retry-gap", defaultRetryGap, "minimum delay between attempts to the same address")
	discoverOnly := flags.Bool("discover-only", false, "only discover responding DNS addresses")
	jsonOutput := flags.Bool("json", false, "write structured server reports as JSON")
	checkWorkers := flags.Int("check-workers", 8, "maximum concurrent server inspections")
	checkTimeout := flags.Duration("check-timeout", 2*time.Second, "deadline for each capability check")
	var tests testOptions
	tests.register(flags)
	silent := flags.Bool("silent", false, "suppress runtime logs; print findings only")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [options] AS-number-or-CIDR-or-IP [target ...]\n", flags.Name())
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	logger := newRuntimeLogger(stderr, *silent)

	if math.IsNaN(*timeout) || math.IsInf(*timeout, 0) || *timeout <= 0 || *timeout >= float64(math.MaxInt64)/float64(time.Second) || time.Duration(*timeout*float64(time.Second)) <= 0 {
		logger.Println("-t must be a positive, representable number of seconds")
		return 2
	}
	opts := scanOptions{timeout: time.Duration(*timeout * float64(time.Second)), rate: *rate, retryGap: *gap}
	if err := opts.validate(); err != nil {
		logger.Println(err)
		return 2
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}

	inspection := inspectionOptions{workers: *checkWorkers, timeout: *checkTimeout, port: 53}
	if err := inspection.validate(); err != nil {
		logger.Println(err)
		return 2
	}
	if *discoverOnly && *jsonOutput {
		logger.Println("-json cannot be combined with -discover-only")
		return 2
	}
	if err := tests.validate(); err != nil {
		logger.Println(err)
		return 2
	}
	checks := tests.checks()
	if *discoverOnly && len(checks) > 0 {
		logger.Println("-discover-only cannot be combined with -test-* options")
		return 2
	}
	onlyAddresses := *discoverOnly || (len(checks) == 0 && !*jsonOutput)
	question, err := discoveryQuestion(*host)
	if err != nil {
		logger.Println(err)
		return 2
	}
	if services.lookup == nil {
		var ripe *ripeClient
		services.lookup = func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
			if ripe == nil {
				ripe = newRIPEClient(logger)
			}
			return ripe.prefixes(ctx, asn)
		}
	}
	contained, err := resolveTargets(ctx, flags.Args(), func(ctx context.Context, asn string) ([]netip.Prefix, error) {
		return services.lookup(ctx, asn, logger)
	}, logger)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		logger.Println(err)
		if errors.Is(err, errTargetSyntax) {
			return 2
		}
		return 1
	}
	if ctx.Err() != nil {
		return 0
	}
	logScanScope(logger, contained)
	output := io.Writer(io.Discard)
	if onlyAddresses {
		output = stdout
	}
	if services.discover == nil {
		services.discover = discover
	}
	found, err := services.discover(ctx, contained, question, opts, output)
	if err == nil && !onlyAddresses {
		var reports []serverReport
		reports, err = inspectServers(ctx, found, checks, inspection)
		if err == nil {
			err = writeReports(stdout, reports, *jsonOutput)
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Println(err)
		return 1
	}
	return 0
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
