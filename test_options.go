package main

import (
	"errors"
	"flag"
	"github.com/miekg/dns"
	"strings"
)

// Test selection is explicit; repeated names append independent checks.
type testOptions struct {
	axfr        namesFlag
	axfrRecords int
	axfrBytes   int
	version     bool
	tcp         bool
	dnssec      bool
	nsid        bool
	dnssecValid string
	dnssecBogus string
	authorities namesFlag
	recursion   namesFlag
}

func (o *testOptions) register(fs *flag.FlagSet) {
	fs.Var(&o.axfr, "test-axfr", "zone apex to test for TCP AXFR (repeatable)")
	fs.IntVar(&o.axfrRecords, "axfr-max-records", defaultAXFRRecords, "maximum records per AXFR check, including SOAs")
	fs.IntVar(&o.axfrBytes, "axfr-max-bytes", defaultAXFRBytes, "maximum DNS TCP bytes per AXFR check, including framing")
	fs.BoolVar(&o.nsid, "test-nsid", false, "request the EDNS Name Server Identifier")
	fs.BoolVar(&o.dnssec, "test-dnssec", false, "probe recursive DNSSEC validation")
	fs.StringVar(&o.dnssecValid, "dnssec-valid-name", "good-a.test.dnssec-tools.org.", "known valid signed A name for -test-dnssec")
	fs.StringVar(&o.dnssecBogus, "dnssec-bogus-name", "badsign-a.test.dnssec-tools.org.", "known bogus signed A name for -test-dnssec")
	fs.BoolVar(&o.tcp, "test-tcp", false, "test DNS over TCP")
	fs.BoolVar(&o.version, "test-version", false, "query self-reported DNS version")
	fs.Var(&o.authorities, "test-authority", "domain to test for authoritative answers (repeatable)")
	fs.Var(&o.recursion, "test-recursion", "external A name to test for recursive answering (repeatable)")
}

func (o testOptions) checks() []serverCheck {
	var checks []serverCheck
	if o.nsid {
		checks = append(checks, nsidCheck{})
	}
	if o.dnssec {
		checks = append(checks, dnssecCheck{valid: dns.Fqdn(o.dnssecValid), bogus: dns.Fqdn(o.dnssecBogus)})
	}
	if o.tcp {
		checks = append(checks, tcpCheck{})
	}
	if o.version {
		checks = append(checks, versionCheck{})
	}
	for _, name := range o.authorities {
		checks = append(checks, authorityCheck{name: name})
	}
	for _, name := range o.recursion {
		checks = append(checks, recursionCheck{name: name})
	}
	checks = append(checks, axfrChecks(o.axfr, o.axfrRecords, o.axfrBytes)...)
	return checks
}

func (o testOptions) validate() error {
	if o.dnssec {
		for _, name := range []string{o.dnssecValid, o.dnssecBogus} {
			if _, err := makeQuestion(name); err != nil {
				return err
			}
		}
		if strings.EqualFold(dns.Fqdn(o.dnssecValid), dns.Fqdn(o.dnssecBogus)) {
			return errors.New("DNSSEC valid and bogus names must differ")
		}
	}
	if o.axfrRecords < 2 || o.axfrBytes < 512 {
		return errors.New("AXFR limits require at least 2 records and 512 bytes")
	}
	return nil
}
