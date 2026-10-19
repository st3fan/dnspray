package main

import (
	"io"
	"log"
	"math/big"
	"net/netip"
)

func newRuntimeLogger(stderr io.Writer, silent bool) *log.Logger {
	if silent {
		stderr = io.Discard
	}
	return log.New(stderr, "", log.LstdFlags)
}

// The resolver supplies a sorted, disjoint scope. Arbitrary precision handles
// IPv6 /0 and mixed families without overflowing an address counter.
func logScanScope(logger *log.Logger, prefixes []netip.Prefix) {
	if logger == nil {
		return
	}
	var v4, v6 big.Int
	v4Prefixes, v6Prefixes := 0, 0
	for _, prefix := range prefixes {
		logger.Printf("Using prefix %s", prefix)
		count := new(big.Int).Lsh(big.NewInt(1), uint(prefix.Addr().BitLen()-prefix.Bits()))
		if prefix.Addr().Is4() {
			v4.Add(&v4, count)
			v4Prefixes++
		} else {
			v6.Add(&v6, count)
			v6Prefixes++
		}
	}
	total := new(big.Int).Add(&v4, &v6)
	logger.Printf("Starting scan: %d prefixes, %s addresses (IPv4: %d prefixes, %s addresses; IPv6: %d prefixes, %s addresses)", len(prefixes), total, v4Prefixes, &v4, v6Prefixes, &v6)
}
