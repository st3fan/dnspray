package main

import "testing"

func TestValidResponse(t *testing.T) {
	query, err := makeQuestion("www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	query[0], query[1] = 0x12, 0x34

	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
		want   bool
	}{
		{name: "answer", change: func(b []byte) []byte { return b }, want: true},
		{name: "NXDOMAIN", change: func(b []byte) []byte { b[3] = 3; return b }, want: true},
		{name: "REFUSED", change: func(b []byte) []byte { b[3] = 5; return b }, want: true},
		{name: "wrong transaction ID", change: func(b []byte) []byte { b[0] ^= 1; return b }},
		{name: "wrong question", change: func(b []byte) []byte { b[13] ^= 1; return b }},
		{name: "not a response", change: func(b []byte) []byte { b[2] &^= 0x80; return b }},
		{name: "truncated packet", change: func(b []byte) []byte { return b[:11] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := append([]byte(nil), query...)
			response[2] |= 0x80
			response = tc.change(response)
			if got := validResponse(query, response); got != tc.want {
				t.Fatalf("validResponse() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInputs(t *testing.T) {
	prefixes, err := parsePrefixes([]string{"192.0.2.123/24", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixes[0].String(); got != "192.0.2.0/24" {
		t.Fatalf("masked prefix = %q", got)
	}
	if got := prefixes[1].String(); got != "2001:db8::1/128" {
		t.Fatalf("single address = %q", got)
	}
	if _, err := makeQuestion("bad..name"); err == nil {
		t.Fatal("expected invalid hostname to fail")
	}
}
