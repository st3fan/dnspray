package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultRIPELifetime = 8 * time.Hour

// This is a private application cache: s-maxage governs shared caches only.
// Missing freshness uses the requested fallback; invalid freshness is stale.
func ripeFreshness(headers http.Header, started, fetched time.Time) (time.Time, bool) {
	lifetime := defaultRIPELifetime
	var maxAge string
	maxAgeCount := 0
	noCache, noStore, invalid := false, false, false
	for _, line := range headers.Values("Cache-Control") {
		directives, valid := cacheDirectives(line)
		if !valid {
			invalid = true
		}
		for _, part := range directives {
			key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "no-cache":
				noCache = true
			case "no-store":
				noStore = true
			case "max-age":
				maxAgeCount++
				maxAge = strings.TrimSpace(value)
			}
		}
	}
	if noCache || noStore || invalid {
		return fetched, noStore
	}
	var date time.Time
	if values := headers.Values("Date"); len(values) > 0 {
		if len(values) != 1 {
			return fetched, false
		}
		var err error
		date, err = http.ParseTime(values[0])
		if err != nil {
			return fetched, false
		}
	}
	if maxAgeCount > 0 {
		var valid bool
		lifetime, valid = deltaSeconds(maxAge)
		if maxAgeCount != 1 || !valid {
			return fetched, false
		}
	} else if values := headers.Values("Expires"); len(values) > 0 {
		if len(values) != 1 {
			return fetched, false
		}
		expires, err := http.ParseTime(values[0])
		if err != nil {
			return fetched, false
		}
		base := date
		if base.IsZero() {
			base = fetched
		}
		lifetime = expires.Sub(base)
	}
	age := time.Duration(0)
	if values := headers.Values("Age"); len(values) > 0 {
		if len(values) != 1 {
			return fetched, false
		}
		var valid bool
		age, valid = deltaSeconds(strings.TrimSpace(values[0]))
		if !valid {
			return fetched, false
		}
	}
	delay := max(time.Duration(0), fetched.Sub(started))
	age = saturatingAdd(age, delay)
	if !date.IsZero() {
		age = max(age, max(time.Duration(0), fetched.Sub(date)))
	}
	remaining := time.Duration(0)
	if lifetime > age {
		remaining = lifetime - age
	}
	return fetched.Add(remaining), false
}

func deltaSeconds(value string) (time.Duration, bool) {
	// Quoted delta-seconds are tolerated for Cache-Control interoperability.
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	if value == "" {
		return 0, false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	if seconds > uint64(math.MaxInt64/int64(time.Second)) {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(seconds) * time.Second, true
}

func saturatingAdd(a, b time.Duration) time.Duration {
	if a > time.Duration(math.MaxInt64)-b {
		return time.Duration(math.MaxInt64)
	}
	return a + b
}

// Commas inside quoted extension values are not directive separators.
func cacheDirectives(line string) ([]string, bool) {
	var result []string
	start := 0
	quoted, escaped := false, false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if quoted && ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			quoted = !quoted
		}
		if ch == ',' && !quoted {
			result = append(result, line[start:i])
			start = i + 1
		}
	}
	result = append(result, line[start:])
	return result, !quoted && !escaped
}
