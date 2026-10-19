package main

import (
	"net/http"
	"testing"
	"time"
)

func TestRIPEFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		headers     http.Header
		delay, want time.Duration
		noStore     bool
	}{
		{"fallback", nil, 0, 8 * time.Hour, false},
		{"max age", http.Header{"Cache-Control": {"public, max-age=3600"}}, 0, time.Hour, false},
		{"quoted", http.Header{"Cache-Control": {"max-age=\"3600\""}}, 0, time.Hour, false},
		{"longer than fallback", http.Header{"Cache-Control": {"max-age=86400"}}, 0, 24 * time.Hour, false},
		{"precedence", http.Header{"Cache-Control": {"max-age=3600"}, "Expires": {"invalid"}}, 0, time.Hour, false},
		{"expires", http.Header{"Date": {now.Format(http.TimeFormat)}, "Expires": {now.Add(time.Hour).Format(http.TimeFormat)}}, 0, time.Hour, false},
		{"expires no date", http.Header{"Expires": {now.Add(time.Hour).Format(http.TimeFormat)}}, 0, time.Hour, false},
		{"expired", http.Header{"Expires": {now.Add(-time.Hour).Format(http.TimeFormat)}}, 0, 0, false},
		{"age", http.Header{"Cache-Control": {"max-age=3600"}, "Age": {"120"}}, 0, 58 * time.Minute, false},
		{"age with delay", http.Header{"Cache-Control": {"max-age=3600"}, "Age": {"120"}}, 30 * time.Second, 57*time.Minute + 30*time.Second, false},
		{"apparent age", http.Header{"Cache-Control": {"max-age=3600"}, "Date": {now.Add(-3 * time.Minute).Format(http.TimeFormat)}, "Age": {"120"}}, 0, 57 * time.Minute, false},
		{"stale age", http.Header{"Cache-Control": {"max-age=60"}, "Age": {"120"}}, 0, 0, false},
		{"fallback aged", http.Header{"Age": {"3600"}}, 0, 7 * time.Hour, false},
		{"no cache", http.Header{"Cache-Control": {"no-cache, max-age=3600"}}, 0, 0, false},
		{"no cache fields", http.Header{"Cache-Control": {"no-cache=\"Date, Age\", max-age=3600"}}, 0, 0, false},
		{"no store", http.Header{"Cache-Control": {"no-store, max-age=3600"}}, 0, 0, true},
		{"max age zero", http.Header{"Cache-Control": {"max-age=0"}}, 0, 0, false},
		{"shared cache", http.Header{"Cache-Control": {"s-maxage=60"}}, 0, 8 * time.Hour, false},
		{"negative max age", http.Header{"Cache-Control": {"max-age=-1"}}, 0, 0, false},
		{"missing max age value", http.Header{"Cache-Control": {"max-age"}}, 0, 0, false},
		{"invalid max age", http.Header{"Cache-Control": {"max-age=invalid"}}, 0, 0, false},
		{"duplicate max age", http.Header{"Cache-Control": {"max-age=60", "max-age=60"}}, 0, 0, false},
		{"conflicting max age", http.Header{"Cache-Control": {"max-age=60, max-age=120"}}, 0, 0, false},
		{"invalid expires", http.Header{"Expires": {"invalid"}}, 0, 0, false},
		{"duplicate expires", http.Header{"Expires": {now.Format(http.TimeFormat), now.Format(http.TimeFormat)}}, 0, 0, false},
		{"invalid age", http.Header{"Age": {"-1"}}, 0, 0, false},
		{"duplicate age", http.Header{"Age": {"1", "2"}}, 0, 0, false},
		{"invalid date", http.Header{"Date": {"invalid"}}, 0, 0, false},
		{"duplicate date", http.Header{"Date": {now.Format(http.TimeFormat), now.Format(http.TimeFormat)}}, 0, 0, false},
		{"quoted extension", http.Header{"Cache-Control": {`extension="a,max-age=86400,b"`}}, 0, 8 * time.Hour, false},
		{"quoted extension no-store text", http.Header{"Cache-Control": {`extension="a,no-store,b", max-age=60`}}, 0, time.Minute, false},
		{"unclosed quote", http.Header{"Cache-Control": {`max-age="60`}}, 0, 0, false},
		{"huge lifetime", http.Header{"Cache-Control": {"max-age=18446744073709551615"}}, 0, time.Duration(1<<63 - 1), false},
		{"overflow age", http.Header{"Age": {"18446744073709551615"}}, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires, noStore := ripeFreshness(tc.headers, now.Add(-tc.delay), now)
			if noStore != tc.noStore || expires.Sub(now) != tc.want {
				t.Fatalf("got %v noStore=%t want %v noStore=%t", expires.Sub(now), noStore, tc.want, tc.noStore)
			}
		})
	}
}
