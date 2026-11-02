package main

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExternalResult is the outcome of fetching one external URL.
type ExternalResult struct {
	URL     string
	Where   []string
	Broken  bool   // definitely dead (404, 410, DNS failure)
	Warning string // could be transient (403, 429, 5xx, timeout): reported, not fatal
}

// skipExternal reports URLs that are placeholders or local and must never be fetched.
func skipExternal(u string) bool {
	l := strings.ToLower(u)
	for _, s := range []string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]", "example.com", "example.test", "example.org", "<", ">", "{", "}"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// CheckExternal fetches every URL (HEAD, then GET if HEAD is refused) with bounded concurrency.
func CheckExternal(urls map[string][]string, client *http.Client, workers int) []ExternalResult {
	keys := make([]string, 0, len(urls))
	for u := range urls {
		if !skipExternal(u) {
			keys = append(keys, u)
		}
	}
	sort.Strings(keys)
	results := make([]ExternalResult, len(keys))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, u := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fetchOne(u, urls[u], client)
		}(i, u)
	}
	wg.Wait()
	return results
}

func fetchOne(u string, where []string, client *http.Client) ExternalResult {
	res := ExternalResult{URL: u, Where: where}
	var code int
	var err error
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		var req *http.Request
		req, err = http.NewRequest(method, u, nil)
		if err != nil {
			res.Broken = true
			res.Warning = "invalid URL"
			return res
		}
		req.Header.Set("User-Agent", "local-ai-linkcheck/1 (+https://github.com/marinoscar/local-ai)")
		var resp *http.Response
		resp, err = client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		code = resp.StatusCode
		if code < 400 {
			return res
		}
		if method == http.MethodHead {
			continue // some servers refuse HEAD: retry with GET
		}
	}
	switch {
	case err != nil:
		if dnsErr, ok := err.(interface{ Unwrap() error }); ok {
			if _, isDNS := dnsErr.Unwrap().(*net.DNSError); isDNS {
				res.Broken, res.Warning = true, "DNS lookup failed"
				return res
			}
		}
		if strings.Contains(err.Error(), "no such host") {
			res.Broken, res.Warning = true, "DNS lookup failed"
			return res
		}
		res.Warning = "request failed: " + err.Error()
	case code == 404 || code == 410:
		res.Broken, res.Warning = true, fmt.Sprintf("HTTP %d", code)
	default:
		res.Warning = fmt.Sprintf("HTTP %d (may be transient or bot-blocked)", code)
	}
	return res
}

func newClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
