package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ripeEndpoint = "https://stat.ripe.net/data/ris-prefixes/data.json"
const maxRIPEBody = 8 << 20
const maxRIPECache = maxRIPEBody + (64 << 10)
const ripeTimeout = 15 * time.Second
const ripeCacheVersion = 1

type ripeClient struct {
	client   *http.Client
	endpoint string
	cacheDir string
	now      func() time.Time
	logger   *log.Logger
}

type ripeCache struct {
	Version   int             `json:"version"`
	ASN       string          `json:"asn"`
	FetchedAt time.Time       `json:"fetched_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	Headers   http.Header     `json:"headers"`
	Response  json.RawMessage `json:"response"`
}

type ripeResponse struct {
	Status       string `json:"status"`
	StatusCode   int    `json:"status_code"`
	DataCallName string `json:"data_call_name"`
	Data         struct {
		Resource     string `json:"resource"`
		ListPrefixes bool   `json:"list_prefixes"`
		Prefixes     struct {
			V4 struct {
				Originating *[]string `json:"originating"`
			} `json:"v4"`
		} `json:"prefixes"`
	} `json:"data"`
}

func newRIPEClient(logger *log.Logger) *ripeClient {
	c := &ripeClient{client: &http.Client{Timeout: ripeTimeout}, endpoint: ripeEndpoint, now: time.Now, logger: logger}
	home, err := os.UserHomeDir()
	if err != nil {
		c.warn("cache disabled: %v", err)
	} else if !filepath.IsAbs(home) {
		c.warn("cache disabled: home directory is not absolute")
	} else {
		c.cacheDir = filepath.Join(home, ".cache", "find-nameservers", "ripe")
	}
	return c
}

func (c *ripeClient) warn(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf("RIPE cache: "+format, args...)
	}
}

func (c *ripeClient) prefixes(ctx context.Context, asn string) ([]netip.Prefix, error) {
	canonical, err := canonicalASN(asn)
	if err != nil {
		return nil, err
	}
	asn = canonical
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := c.readCache(asn); ok {
		if c.logger != nil {
			c.logger.Printf("RIPE: cache hit for %s (%d IPv4 prefixes)", asn, len(cached))
		}
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(ctx, ripeTimeout)
	defer cancel()
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("resource", asn)
	query.Set("list_prefixes", "true")
	query.Set("types", "o")
	query.Set("af", "v4")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if c.logger != nil {
		c.logger.Printf("RIPE: fetching IPv4 origin prefixes for %s", asn)
	}
	started := c.now()
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("RIPE request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RIPE HTTP status %d", response.StatusCode)
	}
	body, err := readBounded(response.Body, maxRIPEBody)
	if err != nil {
		return nil, fmt.Errorf("RIPE response: %w", err)
	}
	prefixes, err := parseRIPE(body, asn)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.logger != nil {
		c.logger.Printf("RIPE: fetched %s (%d IPv4 prefixes)", asn, len(prefixes))
	}
	fetched := c.now()
	expires, noStore := ripeFreshness(response.Header, started, fetched)
	if noStore {
		c.removeCache(asn)
	} else {
		entry := ripeCache{Version: ripeCacheVersion, ASN: asn, FetchedAt: fetched, ExpiresAt: expires, Headers: cacheHeaders(response.Header), Response: body}
		if err := c.writeCache(entry); err != nil {
			c.warn("could not save %s: %v", asn, err)
		}
	}
	return prefixes, nil
}

// Preserve all validated origin prefixes for logging; the target resolver
// removes overlaps only after every source has resolved.
func parseRIPE(body []byte, asn string) ([]netip.Prefix, error) {
	var response ripeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("invalid RIPE JSON: %w", err)
	}
	if response.Status != "ok" || response.StatusCode != 200 || response.DataCallName != "ris-prefixes" || !response.Data.ListPrefixes {
		return nil, errors.New("RIPE API response was unsuccessful or missing prefix data")
	}
	resource := response.Data.Resource
	if !strings.HasPrefix(strings.ToUpper(resource), "AS") {
		resource = "AS" + resource
	}
	canonical, err := canonicalASN(resource)
	if err != nil || canonical != asn {
		return nil, errors.New("RIPE response ASN does not match the requested ASN")
	}
	if response.Data.Prefixes.V4.Originating == nil {
		return nil, errors.New("RIPE response is missing the IPv4 origin-prefix array")
	}
	var prefixes []netip.Prefix
	for _, value := range *response.Data.Prefixes.V4.Originating {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid RIPE prefix %q", value)
		}
		if prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return prefixes, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("data exceeds %d-byte limit", limit)
	}
	return data, nil
}

func cacheHeaders(source http.Header) http.Header {
	result := make(http.Header)
	for _, name := range []string{"Cache-Control", "Expires", "Date", "Age"} {
		for _, value := range source.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}

func (c *ripeClient) readCache(asn string) ([]netip.Prefix, bool) {
	if c.cacheDir == "" {
		return nil, false
	}
	file, err := os.Open(filepath.Join(c.cacheDir, asn+".json"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.warn("could not read %s: %v", asn, err)
		}
		return nil, false
	}
	defer file.Close()
	body, err := readBounded(file, maxRIPECache)
	if err != nil {
		c.warn("could not read %s: %v", asn, err)
		return nil, false
	}
	var entry ripeCache
	if json.Unmarshal(body, &entry) != nil || entry.Version != ripeCacheVersion || entry.ASN != asn || entry.FetchedAt.IsZero() || entry.FetchedAt.After(c.now()) || !c.now().Before(entry.ExpiresAt) {
		return nil, false
	}
	// A cache policy requiring refresh must not be overridden by edited or stale
	// expiry metadata. Recomputing also catches malformed freshness directives.
	headerExpiry, noStore := ripeFreshness(entry.Headers, entry.FetchedAt, entry.FetchedAt)
	if noStore || !headerExpiry.After(entry.FetchedAt) || entry.ExpiresAt.After(headerExpiry) {
		return nil, false
	}
	if len(entry.Response) > maxRIPEBody {
		return nil, false
	}
	prefixes, err := parseRIPE(entry.Response, asn)
	return prefixes, err == nil
}

func (c *ripeClient) removeCache(asn string) {
	if c.cacheDir == "" {
		return
	}
	if err := os.Remove(filepath.Join(c.cacheDir, asn+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.warn("could not remove %s: %v", asn, err)
	}
}

func (c *ripeClient) writeCache(entry ripeCache) error {
	if c.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.cacheDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(c.cacheDir, 0700); err != nil {
		return err
	}
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(entry); err != nil {
		return err
	}
	if buffer.Len() > maxRIPECache {
		return errors.New("cache entry exceeds size limit")
	}
	file, err := os.CreateTemp(c.cacheDir, "."+entry.ASN+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := io.WriteString(file, buffer.String()); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(c.cacheDir, entry.ASN+".json"))
}
