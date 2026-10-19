package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"
)

func findingFixture(_ context.Context, _ []netip.Prefix, _ *dns.Msg, _ scanOptions, output io.Writer) ([]foundServer, error) {
	address := netip.MustParseAddr("192.0.2.1")
	if _, err := fmt.Fprintln(output, address); err != nil {
		return nil, err
	}
	return []foundServer{{Address: address}}, nil
}

func TestCLILoggingKeepsFindingsSeparate(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		var baseline string
		for _, silent := range []bool{false, true} {
			var stdout, stderr bytes.Buffer
			args := []string{"192.0.2.0/24", "192.0.2.1", "192.0.2.128/25", "2001:db8::1"}
			if jsonOutput {
				args = append([]string{"-json"}, args...)
			}
			if silent {
				args = append([]string{"-silent"}, args...)
			}
			called := false
			service := cliServices{discover: func(ctx context.Context, prefixes []netip.Prefix, q *dns.Msg, opts scanOptions, out io.Writer) ([]foundServer, error) {
				called = true
				if !silent && !strings.Contains(stderr.String(), "Starting scan: 2 prefixes, 257 addresses (IPv4: 1 prefixes, 256 addresses; IPv6: 1 prefixes, 1 addresses)") {
					t.Error("missing/double-counted scope summary before discovery", stderr.String())
				}
				if len(prefixes) != 2 {
					t.Error(prefixes)
				}
				return findingFixture(ctx, prefixes, q, opts, out)
			}}
			code := runCLI(context.Background(), args, &stdout, &stderr, service)
			if code != 0 || !called {
				t.Fatal(code, stderr.String())
			}
			if silent {
				if stderr.Len() != 0 || stdout.String() != baseline {
					t.Fatal("silent changed findings or leaked logs", stdout.String(), stderr.String())
				}
			} else {
				baseline = stdout.String()
				if !strings.Contains(stderr.String(), "Using prefix 192.0.2.0/24") || strings.Contains(stderr.String(), "Using prefix 192.0.2.128/25") {
					t.Fatal(stderr.String())
				}
			}
			if jsonOutput {
				var reports []serverReport
				if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil || len(reports) != 1 || reports[0].Address.String() != "192.0.2.1" {
					t.Fatal(stdout.String(), err)
				}
			} else if stdout.String() != "192.0.2.1\n" {
				t.Fatal(stdout.String())
			}
		}
	}
}

func TestScopeSummaryIPv6DoesNotOverflow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	service := cliServices{discover: func(context.Context, []netip.Prefix, *dns.Msg, scanOptions, io.Writer) ([]foundServer, error) {
		called = true
		return nil, nil
	}}
	code := runCLI(context.Background(), []string{"::/0", "2001:db8::/32", "0.0.0.0/0", "192.0.2.0/24"}, &stdout, &stderr, service)
	if code != 0 || !called || stdout.Len() != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"Starting scan: 2 prefixes, 340282366920938463463374607436063178752 addresses", "IPv4: 1 prefixes, 4294967296 addresses", "IPv6: 1 prefixes, 340282366920938463463374607431768211456 addresses"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatal(stderr.String())
		}
	}
}

func TestRIPEFetchCacheAndPrefixLogs(t *testing.T) {
	var requests atomic.Int32
	client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24", "192.0.2.128/25", "198.51.100.0/24", "2001:db8::/32"))
	})
	service := cliServices{lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
		client.logger = logger
		return client.prefixes(ctx, asn)
	}, discover: findingFixture}
	for _, cached := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		code := runCLI(context.Background(), []string{"AS64496", "as00064496", "192.0.2.0/24"}, &stdout, &stderr, service)
		if code != 0 || stdout.String() != "192.0.2.1\n" {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		output := stderr.String()
		for _, want := range []string{"AS64496: found IPv4 prefix 192.0.2.0/24", "AS64496: found IPv4 prefix 192.0.2.128/25", "AS64496: found IPv4 prefix 198.51.100.0/24", "AS64496: reusing resolved prefixes", "AS64496: prefix 192.0.2.128/25 is covered by 192.0.2.0/24", "Using prefix 192.0.2.0/24", "Using prefix 198.51.100.0/24", "Starting scan: 2 prefixes, 512 addresses"} {
			if !strings.Contains(output, want) {
				t.Fatal("missing", want, output)
			}
		}
		for _, excluded := range []string{"2001:db8", "203.0.113", "Using prefix 192.0.2.128/25"} {
			if strings.Contains(output, excluded) {
				t.Fatal("unexpected target", output)
			}
		}
		if cached {
			if !strings.Contains(output, "RIPE: cache hit for AS64496") || strings.Contains(output, "RIPE: fetching") {
				t.Fatal(output)
			}
		} else if strings.Count(output, "RIPE: fetching IPv4 origin prefixes for AS64496") != 1 || !strings.Contains(output, "RIPE: fetched AS64496") {
			t.Fatal(output)
		}
		if requests.Load() != 1 {
			t.Fatal("repeated ASN/cache hit fetched again", requests.Load())
		}
	}
}

func TestFoundPrefixCoveredByAnotherTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	service := cliServices{lookup: func(context.Context, string, *log.Logger) ([]netip.Prefix, error) {
		return []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, nil
	}, discover: findingFixture}
	code := runCLI(context.Background(), []string{"AS64496", "192.0.0.0/16"}, &stdout, &stderr, service)
	if code != 0 || !strings.Contains(stderr.String(), "AS64496: prefix 192.0.2.0/24 is covered by 192.0.0.0/16") || !strings.Contains(stderr.String(), "Starting scan: 1 prefixes, 65536 addresses") {
		t.Fatal(code, stderr.String())
	}
}

func TestSilentCacheWarningsAndFetchLogs(t *testing.T) {
	for _, silent := range []bool{false, true} {
		client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24")) })
		badDirectory := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(badDirectory, []byte("file"), 0600); err != nil {
			t.Fatal(err)
		}
		client.cacheDir = filepath.Join(badDirectory, "ripe")
		service := cliServices{lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
			client.logger = logger
			return client.prefixes(ctx, asn)
		}, discover: findingFixture}
		var stdout, stderr bytes.Buffer
		args := []string{"AS64496"}
		if silent {
			args = append([]string{"-silent"}, args...)
		}
		code := runCLI(context.Background(), args, &stdout, &stderr, service)
		if code != 0 || stdout.String() != "192.0.2.1\n" {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		if silent && stderr.Len() != 0 {
			t.Fatal(stderr.String())
		}
		if !silent && !strings.Contains(stderr.String(), "RIPE cache: could not save AS64496") {
			t.Fatal(stderr.String())
		}
	}
}

func TestFailureLogsAndExitStatuses(t *testing.T) {
	for _, mode := range []string{"invalid target", "RIPE error", "scan error", "invalid timeout", "output error"} {
		for _, silent := range []bool{false, true} {
			client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
			service := cliServices{lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
				client.logger = logger
				return client.prefixes(ctx, asn)
			}, discover: findingFixture}
			args := []string{"192.0.2.1"}
			wantCode := 1
			switch mode {
			case "invalid target":
				args = []string{"not-a-target"}
				wantCode = 2
			case "RIPE error":
				args = []string{"192.0.2.1", "AS64496"}
			case "scan error":
				service.discover = func(context.Context, []netip.Prefix, *dns.Msg, scanOptions, io.Writer) ([]foundServer, error) {
					return nil, errors.New("scan failed")
				}
			case "invalid timeout":
				args = []string{"-t", "0", "192.0.2.1"}
				wantCode = 2
			}
			if silent {
				args = append([]string{"-silent"}, args...)
			}
			var stdout, stderr bytes.Buffer
			output := io.Writer(&stdout)
			if mode == "output error" {
				output = failingWriter{}
			}
			code := runCLI(context.Background(), args, output, &stderr, service)
			if code != wantCode || stdout.Len() != 0 {
				t.Fatal(mode, code, stdout.String(), stderr.String())
			}
			if silent && stderr.Len() != 0 {
				t.Fatal("silent leaked error logs", stderr.String())
			}
			if !silent && stderr.Len() == 0 {
				t.Fatal("failure not logged")
			}
			if (mode == "invalid target" || mode == "RIPE error" || mode == "invalid timeout") && (strings.Contains(stderr.String(), "Starting scan") || strings.Contains(stderr.String(), "Using prefix")) {
				t.Fatal("announced unresolved scope", stderr.String())
			}
		}
	}
}

func TestFailedLaterASNDoesNotStartScan(t *testing.T) {
	client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") == "AS64497" {
			w.WriteHeader(503)
			return
		}
		w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
	})
	scanned := false
	service := cliServices{lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
		client.logger = logger
		return client.prefixes(ctx, asn)
	}, discover: func(context.Context, []netip.Prefix, *dns.Msg, scanOptions, io.Writer) ([]foundServer, error) {
		scanned = true
		return nil, nil
	}}
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), []string{"AS64496", "AS64497"}, &stdout, &stderr, service)
	if code != 1 || scanned || stdout.Len() != 0 || strings.Contains(stderr.String(), "Starting scan") || strings.Contains(stderr.String(), "Using prefix") {
		t.Fatal(code, scanned, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "resolve AS64497") || !strings.Contains(stderr.String(), "HTTP status 503") {
		t.Fatal(stderr.String())
	}
}

func TestSilentHelpAndFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		code    int
		message string
	}{
		{[]string{"-silent", "-help"}, 0, "Usage:"},
		{[]string{"-silent", "-unknown"}, 2, "flag provided but not defined"},
		{[]string{"-silent"}, 2, "Usage:"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runCLI(context.Background(), tc.args, &stdout, &stderr, cliServices{}); code != tc.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.message) {
			t.Fatal(code, stdout.String(), stderr.String())
		}
	}
}
