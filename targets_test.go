package main

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func TestCanonicalASN(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"AS1", "AS1"}, {"as00064496", "AS64496"}, {"As4294967295", "AS4294967295"}} {
		got, err := canonicalASN(tc.input)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, input := range []string{"AS", "AS0", "AS-1", "AS+1", "AS1.2", "AS 1", "AS4294967296", "AS184467440737095516160", "64496", "AS１", "AS1/24", "AS../file", " AS1"} {
		if _, err := canonicalASN(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func prefixStrings(prefixes []netip.Prefix) []string {
	var result []string
	for _, p := range prefixes {
		result = append(result, p.String())
	}
	return result
}

func TestResolveMixedTargets(t *testing.T) {
	calls := make(map[string]int)
	lookup := func(_ context.Context, asn string) ([]netip.Prefix, error) {
		calls[asn]++
		switch asn {
		case "AS64496":
			return []netip.Prefix{netip.MustParsePrefix("192.0.2.128/25"), netip.MustParsePrefix("192.0.2.0/24")}, nil
		default:
			return []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, nil
		}
	}
	prefixes, err := resolveTargets(context.Background(), []string{"AS64496", "192.0.2.3", "as00064496", "2001:db8::1", "192.0.2.129/25", "AS64497", "2001:db8::/32", "198.51.100.1/24"}, lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.0/24", "198.51.100.0/24", "2001:db8::/32"}
	if !reflect.DeepEqual(prefixStrings(prefixes), want) || calls["AS64496"] != 1 || calls["AS64497"] != 1 {
		t.Fatal(prefixStrings(prefixes), calls)
	}
}

func TestResolveTargetsBeforeScanning(t *testing.T) {
	calls := 0
	lookup := func(context.Context, string) ([]netip.Prefix, error) { calls++; return nil, errors.New("API failed") }
	prefixes, err := resolveTargets(context.Background(), []string{"AS64496", "not-an-ip"}, lookup, nil)
	if !errors.Is(err, errTargetSyntax) || prefixes != nil || calls != 0 {
		t.Fatal("invalid syntax caused lookup", prefixes, calls, err)
	}
	prefixes, err = resolveTargets(context.Background(), []string{"192.0.2.1", "AS64496"}, lookup, nil)
	if err == nil || prefixes != nil || calls != 1 {
		t.Fatal("returned partial scope", prefixes, calls, err)
	}
	prefixes, err = resolveTargets(context.Background(), []string{"192.0.2.1", "2001:db8::1"}, nil, nil)
	if err != nil || len(prefixes) != 2 {
		t.Fatal("IP-only scan needed API", prefixes, err)
	}
	for _, resolved := range [][]netip.Prefix{nil, {{}}, {netip.MustParsePrefix("2001:db8::/32")}} {
		prefixes, err = resolveTargets(context.Background(), []string{"192.0.2.1", "AS64496"}, func(context.Context, string) ([]netip.Prefix, error) { return resolved, nil }, nil)
		if err == nil || prefixes != nil {
			t.Fatal("returned incomplete/invalid ASN scope", prefixes, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveTargets(ctx, []string{"AS64496"}, lookup, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
