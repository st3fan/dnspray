package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var ripeTestTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func ripeFixtureBody(asn string, prefixes ...string) []byte {
	body := map[string]any{"status": "ok", "status_code": 200, "data_call_name": "ris-prefixes", "data": map[string]any{
		"resource": strings.TrimPrefix(asn, "AS"), "list_prefixes": true, "prefixes": map[string]any{
			"v4": map[string]any{"originating": prefixes, "transiting": []string{"203.0.113.0/24"}},
			"v6": map[string]any{"originating": []string{"2001:db8::/32"}},
		},
	}}
	data, _ := json.Marshal(body)
	return data
}

func ripeFixture(t *testing.T, handler http.HandlerFunc) *ripeClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header()["Date"] = nil; handler(w, r) }))
	t.Cleanup(server.Close)
	return &ripeClient{client: server.Client(), endpoint: server.URL + "/data/ris-prefixes/data.json", cacheDir: t.TempDir(), now: func() time.Time { return ripeTestTime }, warnings: io.Discard}
}

func TestRIPERequestAndCache(t *testing.T) {
	var calls atomic.Int32
	c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/data/ris-prefixes/data.json" || r.URL.Query().Get("resource") != "AS64496" || r.URL.Query().Get("af") != "v4" || r.URL.Query().Get("types") != "o" || r.URL.Query().Get("list_prefixes") != "true" {
			t.Errorf("incorrect request %v", r.URL)
		}
		w.Header().Set("Set-Cookie", "must-not-be-stored")
		w.Write(ripeFixtureBody("AS64496", "192.0.2.3/24", "192.0.2.128/25", "2001:db8::/32"))
	})
	now := ripeTestTime
	c.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		prefixes, err := c.prefixes(context.Background(), "as00064496")
		if err != nil || !reflect.DeepEqual(prefixStrings(prefixes), []string{"192.0.2.0/24"}) {
			t.Fatal(prefixes, err)
		}
		now = now.Add(7 * time.Hour)
	}
	if calls.Load() != 1 {
		t.Fatal("fresh cache was not reused", calls.Load())
	}
	// The previous read must not extend the original expiration.
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("expired entry reused", calls.Load())
	}
	file := filepath.Join(c.cacheDir, "AS64496.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var entry ripeCache
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if !entry.ExpiresAt.Equal(now.Add(defaultRIPELifetime)) || entry.Headers.Get("Set-Cookie") != "" {
		t.Fatal(entry)
	}
	stat, _ := os.Stat(file)
	dir, _ := os.Stat(c.cacheDir)
	if stat.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("cache permissions", stat.Mode(), dir.Mode())
	}
	leftovers, _ := filepath.Glob(filepath.Join(c.cacheDir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}

func TestRIPECachePolicies(t *testing.T) {
	for _, directive := range []string{"max-age=60", "no-cache, max-age=3600", "no-store, max-age=3600"} {
		t.Run(directive, func(t *testing.T) {
			var calls atomic.Int32
			c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", directive)
				w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
			})
			for range 2 {
				if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
					t.Fatal(err)
				}
			}
			want := int32(2)
			if directive == "max-age=60" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatal(calls.Load(), want)
			}
			_, err := os.Stat(filepath.Join(c.cacheDir, "AS64496.json"))
			if strings.Contains(directive, "no-store") && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stored no-store response", err)
			}
		})
	}
}

func TestRIPEDataValidation(t *testing.T) {
	for _, mode := range []string{"wrong ASN", "wrong endpoint", "API error", "HTTP error", "invalid JSON", "invalid prefix", "no v4", "only transiting", "missing list", "missing status", "trailing JSON"} {
		t.Run(mode, func(t *testing.T) {
			c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				body := ripeFixtureBody("AS64496", "192.0.2.0/24")
				switch mode {
				case "wrong ASN":
					body = ripeFixtureBody("AS64497", "192.0.2.0/24")
				case "wrong endpoint":
					body = bytes.Replace(body, []byte("ris-prefixes"), []byte("other"), 1)
				case "API error":
					body = bytes.Replace(body, []byte(`"status":"ok"`), []byte(`"status":"error"`), 1)
				case "HTTP error":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case "invalid JSON":
					body = []byte(`{`)
				case "invalid prefix":
					body = ripeFixtureBody("AS64496", "not-a-prefix")
				case "no v4":
					body = ripeFixtureBody("AS64496", "2001:db8::/32")
				case "only transiting":
					body = ripeFixtureBody("AS64496")
				case "missing list":
					body = bytes.Replace(body, []byte(`"list_prefixes":true`), []byte(`"list_prefixes":false`), 1)
				case "missing status":
					body = bytes.Replace(body, []byte(`"status_code":200`), []byte(`"status_code":0`), 1)
				case "trailing JSON":
					body = append(body, []byte(` {}`)...)
				}
				w.Write(body)
			})
			if prefixes, err := c.prefixes(context.Background(), "AS64496"); err == nil || prefixes != nil {
				t.Fatal(prefixes, err)
			}
			files, _ := os.ReadDir(c.cacheDir)
			if len(files) != 0 {
				t.Fatal("cached unsuccessful response", files)
			}
		})
	}
}

func TestRIPECorruptCacheRecovery(t *testing.T) {
	for _, mode := range []string{"invalid JSON", "wrong ASN", "wrong version", "future retrieval", "invalid response", "edited expiry", "expired", "no-cache metadata"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
			})
			entry := ripeCache{Version: ripeCacheVersion, ASN: "AS64496", FetchedAt: ripeTestTime, ExpiresAt: ripeTestTime.Add(defaultRIPELifetime), Response: ripeFixtureBody("AS64496", "198.51.100.0/24")}
			switch mode {
			case "wrong ASN":
				entry.ASN = "AS64497"
			case "wrong version":
				entry.Version++
			case "future retrieval":
				entry.FetchedAt = ripeTestTime.Add(time.Hour)
			case "invalid response":
				entry.Response = ripeFixtureBody("AS64497", "198.51.100.0/24")
			case "edited expiry":
				entry.ExpiresAt = ripeTestTime.Add(24 * time.Hour)
			case "expired":
				entry.ExpiresAt = ripeTestTime
			case "no-cache metadata":
				entry.Headers = http.Header{"Cache-Control": {"no-cache"}}
			}
			data, _ := json.Marshal(entry)
			if mode == "invalid JSON" {
				data = []byte(`{`)
			}
			if err := os.WriteFile(filepath.Join(c.cacheDir, "AS64496.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			prefixes, err := c.prefixes(context.Background(), "AS64496")
			if err != nil || calls.Load() != 1 || !reflect.DeepEqual(prefixStrings(prefixes), []string{"192.0.2.0/24"}) {
				t.Fatal(prefixes, calls.Load(), err)
			}
		})
	}
}

func TestRIPENoStaleFallback(t *testing.T) {
	var unavailable atomic.Bool
	c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
	})
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	c.now = func() time.Time { return ripeTestTime.Add(9 * time.Hour) }
	if prefixes, err := c.prefixes(context.Background(), "AS64496"); err == nil || prefixes != nil {
		t.Fatal("used stale targets", prefixes, err)
	}
}

func TestRIPEBodyLimitAndCacheIOFailure(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat(" ", maxRIPEBody+1)) })
		if _, err := c.prefixes(context.Background(), "AS64496"); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatal(err)
		}
	})
	t.Run("cache unavailable", func(t *testing.T) {
		c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24")) })
		path := filepath.Join(t.TempDir(), "not-a-directory")
		os.WriteFile(path, []byte("file"), 0600)
		c.cacheDir = filepath.Join(path, "ripe")
		var warnings bytes.Buffer
		c.warnings = &warnings
		prefixes, err := c.prefixes(context.Background(), "AS64496")
		if err != nil || len(prefixes) != 1 || !strings.Contains(warnings.String(), "could not save") {
			t.Fatal(prefixes, err, warnings.String())
		}
	})
}

func TestRIPECancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			received := make(chan struct{})
			c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) { close(received); <-r.Context().Done() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" {
				c.client.Timeout = 30 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { _, err := c.prefixes(ctx, "AS64496"); done <- err }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("no API request")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected error")
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("lookup did not stop")
			}
		})
	}
}

func TestRIPEAtomicCacheWrites(t *testing.T) {
	c := &ripeClient{cacheDir: t.TempDir(), now: func() time.Time { return ripeTestTime }, warnings: io.Discard}
	entry := ripeCache{Version: ripeCacheVersion, ASN: "AS64496", FetchedAt: ripeTestTime, ExpiresAt: ripeTestTime.Add(defaultRIPELifetime), Response: ripeFixtureBody("AS64496", "192.0.2.0/24")}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				if err := c.writeCache(entry); err != nil {
					t.Error(err)
				}
				prefixes, ok := c.readCache("AS64496")
				if !ok || len(prefixes) != 1 {
					t.Error("partial cache file observed")
				}
			}
		}()
	}
	wg.Wait()
	entries, _ := os.ReadDir(c.cacheDir)
	if len(entries) != 1 || entries[0].Name() != "AS64496.json" {
		t.Fatal(entries)
	}
}

func TestRIPECacheLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "other"))
	c := newRIPEClient(io.Discard)
	if c.cacheDir != filepath.Join(home, ".cache", "find-nameservers", "ripe") {
		t.Fatal(c.cacheDir)
	}
}

func TestRIPEPrefixValidation(t *testing.T) {
	prefixes, err := parseRIPE(ripeFixtureBody("AS64496", "192.0.2.9/24", "2001:db8::/32"), "AS64496")
	if err != nil || !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) {
		t.Fatal(prefixes, err)
	}
}

func TestRIPENoStoreRemovesPreviousEntry(t *testing.T) {
	var noStore atomic.Bool
	c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if noStore.Load() {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
	})
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
		t.Fatal(err)
	}
	noStore.Store(true)
	c.now = func() time.Time { return ripeTestTime.Add(9 * time.Hour) }
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.cacheDir, "AS64496.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old entry retained after no-store", err)
	}
}

func TestRIPECacheUsesExplicitLongLifetime(t *testing.T) {
	var calls atomic.Int32
	c := ripeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(ripeFixtureBody("AS64496", "192.0.2.0/24"))
	})
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return ripeTestTime.Add(9 * time.Hour) }
	if _, err := c.prefixes(context.Background(), "AS64496"); err != nil || calls.Load() != 1 {
		t.Fatal("header lifetime did not override fallback", err, calls.Load())
	}
}

func TestRIPECacheWithoutAbsoluteHome(t *testing.T) {
	for _, home := range []string{"", "relative-home"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			var warnings bytes.Buffer
			c := newRIPEClient(&warnings)
			if c.cacheDir != "" || !strings.Contains(warnings.String(), "cache disabled") {
				t.Fatal(c.cacheDir, warnings.String())
			}
		})
	}
}

func TestRIPECacheReadAndBodyBounds(t *testing.T) {
	c := &ripeClient{cacheDir: t.TempDir(), now: func() time.Time { return ripeTestTime }, warnings: io.Discard}
	path := filepath.Join(c.cacheDir, "AS64496.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxRIPECache+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.readCache("AS64496"); ok {
		t.Fatal("accepted oversized cache")
	}
	if _, err := readBounded(io.MultiReader(strings.NewReader("abc"), failingReader{}), 10); err == nil {
		t.Fatal("lost body read error")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
