package main

import "flag"

// Test selection is explicit; repeated names append independent checks.
type testOptions struct {
	version     bool
	authorities namesFlag
	recursion   namesFlag
}

func (o *testOptions) register(fs *flag.FlagSet) {
	fs.BoolVar(&o.version, "test-version", false, "query self-reported DNS version")
	fs.Var(&o.authorities, "test-authority", "domain to test for authoritative answers (repeatable)")
	fs.Var(&o.recursion, "test-recursion", "external A name to test for recursive answering (repeatable)")
}

func (o testOptions) checks() []serverCheck {
	var checks []serverCheck
	if o.version {
		checks = append(checks, versionCheck{})
	}
	for _, name := range o.authorities {
		checks = append(checks, authorityCheck{name: name})
	}
	for _, name := range o.recursion {
		checks = append(checks, recursionCheck{name: name})
	}
	return checks
}
