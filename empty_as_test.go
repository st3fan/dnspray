package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func TestEmptyASNCombinedScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"middle", []string{"AS64496", "AS64497", "AS64498"}, []string{"192.0.2.0/24", "198.51.100.0/24"}},
		{"first", []string{"AS64497", "AS64496"}, []string{"192.0.2.0/24"}},
		{"last", []string{"AS64496", "AS64497"}, []string{"192.0.2.0/24"}},
		{"repeated and mixed", []string{"AS64497", "as00064497", "AS64499", "192.0.2.1", "198.51.100.0/24", "2001:db8::1"}, []string{"192.0.2.1/32", "198.51.100.0/24", "2001:db8::1/128"}},
		{"all empty", []string{"AS64497", "AS64499"}, nil},
	} {
		for _, silent := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: " silent"}[silent], func(t *testing.T) {
				calls := make(map[string]int)
				client := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
					asn := r.URL.Query().Get("resource")
					calls[asn]++
					switch asn {
					case "AS64496":
						w.Write(ripeFixtureBody(asn, "192.0.2.0/24"))
					case "AS64498":
						w.Write(ripeFixtureBody(asn, "198.51.100.0/24"))
					default:
						w.Write(ripeFixtureBody(asn))
					}
				})
				var stdout, stderr bytes.Buffer
				scanned := false
				args := append([]string{"-discover-only"}, tc.args...)
				if silent {
					args = append([]string{"-silent"}, args...)
				}
				code := runCLI(context.Background(), args, &stdout, &stderr, cliServices{
					lookup: func(ctx context.Context, asn string, logger *log.Logger) ([]netip.Prefix, error) {
						client.logger = logger
						return client.prefixes(ctx, asn)
					},
					discover: func(_ context.Context, prefixes []netip.Prefix, _ *dns.Msg, _ scanOptions, _ io.Writer) ([]foundServer, error) {
						scanned = true
						if !reflect.DeepEqual(prefixStrings(prefixes), tc.want) {
							t.Errorf("scope %v want %v", prefixStrings(prefixes), tc.want)
						}
						return nil, nil
					},
				})
				if tc.want == nil {
					if code != 1 || scanned || strings.Contains(stderr.String(), "Starting scan:") {
						t.Fatal(code, scanned, stderr.String())
					}
					if !silent && !strings.Contains(stderr.String(), "no scannable targets") {
						t.Fatal(stderr.String())
					}
				} else if code != 0 || !scanned {
					t.Fatal(code, scanned, stderr.String())
				}
				if silent {
					if stderr.Len() != 0 {
						t.Fatal(stderr.String())
					}
				} else {
					if !strings.Contains(stderr.String(), "no originated IPv4 prefixes; skipping") {
						t.Fatal(stderr.String())
					}
					if tc.name == "middle" && !strings.Contains(stderr.String(), "512 addresses") {
						t.Fatal(stderr.String())
					}
				}
				for asn, n := range calls {
					if n != 1 {
						t.Fatal("repeated lookup", asn, n)
					}
				}
				if stdout.Len() != 0 {
					t.Fatal("logging leaked to stdout", stdout.String())
				}
			})
		}
	}
}
