package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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
func resolveTargets(ctx context.Context, args []string, lookup asnLookup, logger *log.Logger) ([]netip.Prefix, error) {
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
	type asnScope struct {
		asn      string
		prefixes []netip.Prefix
	}
	var foundScopes []asnScope
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if target.asn == "" {
			prefixes = append(prefixes, target.prefix)
			continue
		}
		if expanded[target.asn] {
			if logger != nil {
				logger.Printf("%s: reusing resolved prefixes", target.asn)
			}
			continue
		}
		resolved, err := lookup(ctx, target.asn)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", target.asn, err)
		}
		expanded[target.asn] = true
		if len(resolved) == 0 {
			if logger != nil {
				logger.Printf("%s: no originated IPv4 prefixes; skipping", target.asn)
			}
			continue
		}
		normalized := make([]netip.Prefix, 0, len(resolved))
		for _, prefix := range resolved {
			if !prefix.IsValid() || !prefix.Addr().Is4() {
				return nil, fmt.Errorf("resolve %s: invalid IPv4 prefix in lookup", target.asn)
			}
			normalized = append(normalized, prefix.Masked())
		}
		if logger != nil {
			for _, prefix := range normalized {
				logger.Printf("%s: found IPv4 prefix %s", target.asn, prefix)
			}
		}
		prefixes = append(prefixes, normalized...)
		foundScopes = append(foundScopes, asnScope{target.asn, normalized})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(prefixes) == 0 {
		return nil, errors.New("no scannable targets: no originated IPv4 prefixes found")
	}
	prefixes = uniquePrefixes(prefixes)
	if logger != nil {
		for _, scope := range foundScopes {
			for _, prefix := range scope.prefixes {
				index := sort.Search(len(prefixes), func(i int) bool { return prefixes[i].Addr().Compare(prefix.Addr()) > 0 }) - 1
				if index >= 0 && prefixes[index] != prefix && prefixes[index].Bits() <= prefix.Bits() && prefixes[index].Contains(prefix.Addr()) {
					logger.Printf("%s: prefix %s is covered by %s; no separate scan needed", scope.asn, prefix, prefixes[index])
				}
			}
		}
	}
	return prefixes, nil
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
