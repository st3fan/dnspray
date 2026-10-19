package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

var errTargetSyntax = errors.New("invalid target")

type asnLookup func(context.Context, string) ([]netip.Prefix, error)
type scanTarget struct {
	asn    string
	prefix netip.Prefix
}

func canonicalASN(value string) (string, error) {
	if len(value) < 3 || !strings.EqualFold(value[:2], "AS") {
		return "", fmt.Errorf("invalid ASN %q: expected AS followed by a decimal number", value)
	}
	digits := value[2:]
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("invalid ASN %q: expected decimal digits", value)
		}
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || n == 0 {
		return "", fmt.Errorf("invalid ASN %q: number must be between 1 and 4294967295", value)
	}
	return "AS" + strconv.FormatUint(n, 10), nil
}

// Validate every argument before making any lookups, and resolve the complete
// scope before the caller starts discovery. Errors never return a partial scope.
func resolveTargets(ctx context.Context, args []string, lookup asnLookup) ([]netip.Prefix, error) {
	targets := make([]scanTarget, 0, len(args))
	for _, arg := range args {
		if len(arg) >= 2 && strings.EqualFold(arg[:2], "AS") {
			asn, err := canonicalASN(arg)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", errTargetSyntax, err)
			}
			targets = append(targets, scanTarget{asn: asn})
		} else {
			prefixes, err := parsePrefixes([]string{arg})
			if err != nil {
				return nil, fmt.Errorf("%w: %v", errTargetSyntax, err)
			}
			targets = append(targets, scanTarget{prefix: prefixes[0]})
		}
	}
	var prefixes []netip.Prefix
	expanded := make(map[string]bool)
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if target.asn == "" {
			prefixes = append(prefixes, target.prefix)
			continue
		}
		if expanded[target.asn] {
			continue
		}
		resolved, err := lookup(ctx, target.asn)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", target.asn, err)
		}
		if len(resolved) == 0 {
			return nil, fmt.Errorf("resolve %s: no originated IPv4 prefixes", target.asn)
		}
		for _, prefix := range resolved {
			if !prefix.IsValid() || !prefix.Addr().Is4() {
				return nil, fmt.Errorf("resolve %s: invalid IPv4 prefix in lookup", target.asn)
			}
			prefixes = append(prefixes, prefix.Masked())
		}
		expanded[target.asn] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return uniquePrefixes(prefixes), nil
}

func uniquePrefixes(prefixes []netip.Prefix) []netip.Prefix {
	sort.Slice(prefixes, func(i, j int) bool {
		if prefixes[i].Addr() == prefixes[j].Addr() {
			return prefixes[i].Bits() < prefixes[j].Bits()
		}
		return prefixes[i].Addr().Less(prefixes[j].Addr())
	})
	result := prefixes[:0]
	for _, prefix := range prefixes {
		if len(result) > 0 {
			previous := result[len(result)-1]
			if previous.Bits() <= prefix.Bits() && previous.Contains(prefix.Addr()) {
				continue
			}
		}
		result = append(result, prefix)
	}
	return result
}
