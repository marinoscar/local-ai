package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The --online checks (catalog.md section 6): HEAD every URL and Hugging Face file, compare
// Content-Length with size_bytes and the Hugging Face LFS oid with sha256. CI runs this weekly,
// never per PR, and the checks run only when the offline rules pass.
//
// Every finding is LAI-CAT-011 (pinning problem): a size or oid that does not match the pinned
// value, or a file that cannot be fetched at all, means the catalog entry no longer describes
// reality. A network failure is reported as such, so re-running the job tells flakes from rot.

// Default hosts of the online checks.
const (
	DefaultHFEndpoint     = "https://huggingface.co"
	DefaultGitHubEndpoint = "https://github.com"
)

const (
	onlineTimeout     = 30 * time.Second
	onlineConcurrency = 4
	userAgent         = "aictl-catalog-lint"
)

// onlineCheck is one URL to HEAD and what to compare it with.
type onlineCheck struct {
	file string
	path string
	url  string
	// hf: read X-Linked-Size / X-Linked-Etag from the first response (no redirect followed).
	hf    bool
	size  int64  // size_bytes to compare, 0 = none
	oid   string // sha256 to compare with the LFS oid, "" = none
	label string
}

func checkOnline(cat *Catalog, opts LintOptions) []Problem {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: onlineTimeout}
	}
	hfBase := strings.TrimRight(orDefault(opts.HFEndpoint, DefaultHFEndpoint), "/")
	ghBase := strings.TrimRight(orDefault(opts.GitHubEndpoint, DefaultGitHubEndpoint), "/")

	checks := onlineChecks(cat, hfBase, ghBase)
	conc := opts.Concurrency
	if conc <= 0 {
		conc = onlineConcurrency
	}

	var (
		mu    sync.Mutex
		out   []Problem
		wg    sync.WaitGroup
		slots = make(chan struct{}, conc)
	)
	for _, c := range checks {
		wg.Add(1)
		slots <- struct{}{}
		go func(c onlineCheck) {
			defer wg.Done()
			defer func() { <-slots }()
			if msgs := runCheck(ctx, client, c); len(msgs) > 0 {
				mu.Lock()
				for _, m := range msgs {
					out = append(out, Problem{Code: CodePinning, File: c.file, Path: c.path, Message: oneLine(m)})
				}
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	return out
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func onlineChecks(cat *Catalog, hfBase, ghBase string) []onlineCheck {
	var cs []onlineCheck
	for i, m := range cat.Models {
		for j, f := range m.Files {
			p := fmt.Sprintf("models[%d].files[%d].name", i, j)
			switch m.Source.Type {
			case "huggingface":
				u := hfBase + "/" + m.Source.Repo + "/resolve/" + m.Source.Revision + "/" + escapePath(f.Name)
				cs = append(cs, onlineCheck{file: FileModels, path: p, url: u, hf: true, size: f.SizeBytes, oid: f.SHA256})
			case "url":
				cs = append(cs, onlineCheck{file: FileModels, path: fmt.Sprintf("models[%d].files[%d].url", i, j), url: f.URL, size: f.SizeBytes})
			}
		}
	}
	for i, c := range cat.Content {
		sp := fmt.Sprintf("content[%d].source", i)
		src := c.Source
		switch src.Type {
		case "kiwix":
			cs = append(cs, onlineCheck{file: FileArk, path: sp + ".base_url", url: src.BaseURL})
		case "url":
			if src.URL != "" {
				cs = append(cs, onlineCheck{file: FileArk, path: sp + ".url", url: src.URL})
			}
			for j, f := range src.Files {
				cs = append(cs, onlineCheck{file: FileArk, path: fmt.Sprintf("%s.files[%d].url", sp, j), url: f.URL})
			}
		case "github_release":
			u := ghBase + "/" + src.Repo + "/releases/download/" + url.PathEscape(src.Tag) + "/" + url.PathEscape(src.Asset)
			cs = append(cs, onlineCheck{file: FileArk, path: sp + ".asset", url: u})
		case "git_mirror":
			cs = append(cs, onlineCheck{file: FileArk, path: sp + ".url", url: strings.TrimRight(src.URL, "/") + "/info/refs?service=git-upload-pack"})
		}
	}
	return cs
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func head(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, onlineTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return resp, nil
}

// runCheck returns the problems of one URL (empty = fine).
func runCheck(ctx context.Context, client *http.Client, c onlineCheck) []string {
	var resp *http.Response
	var err error
	if c.hf {
		// Hugging Face answers LFS files with a redirect that carries X-Linked-Size and
		// X-Linked-Etag (the sha256); do not follow it for those headers.
		nr := *client
		nr.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err = head(ctx, &nr, c.url)
		if err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.Header.Get("X-Linked-Size") == "" {
			resp, err = head(ctx, client, c.url) // not an LFS file: follow it for Content-Length
		}
	} else {
		resp, err = head(ctx, client, c.url)
	}
	if err != nil {
		return []string{fmt.Sprintf("could not check %s: %v", c.url, urlErr(err))}
	}
	if resp.StatusCode >= 400 {
		return []string{fmt.Sprintf("%s answered HTTP %d", c.url, resp.StatusCode)}
	}
	var out []string
	if c.size > 0 {
		got := int64(-1)
		if v := resp.Header.Get("X-Linked-Size"); c.hf && v != "" {
			got = parseInt(v)
		} else if resp.StatusCode < 300 {
			got = resp.ContentLength
		}
		if got >= 0 && got != c.size {
			out = append(out, fmt.Sprintf("size_bytes is %d but the server reports %d for %s", c.size, got, c.url))
		}
	}
	if c.hf && c.oid != "" {
		etag := strings.Trim(strings.TrimPrefix(resp.Header.Get("X-Linked-Etag"), "W/"), `"`)
		if isHex(etag, 64) && etag != c.oid {
			out = append(out, fmt.Sprintf("sha256 is %s but the Hugging Face LFS oid is %s", c.oid, etag))
		}
	}
	return out
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// urlErr unwraps *url.Error so the message does not repeat the URL.
func urlErr(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
