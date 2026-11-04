package whisper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testdataFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseVerboseJSON(t *testing.T) {
	tests := []struct {
		file string
		text string
		lang string
		dur  float64
		prob *float64
	}{
		{"inference_verbose_json.sample.json", "How do I clean a deep cut?", "en", 3.2, ptr(0.9734)},
		{"inference_verbose_json.no_probability.json", "¿Cómo purifico el agua?", "es", 2.5, nil},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			r, err := ParseVerboseJSON(testdataFile(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if r.Text != tc.text || r.Language != tc.lang || r.Duration != tc.dur {
				t.Fatalf("got %+v", r)
			}
			if (r.LanguageProbability == nil) != (tc.prob == nil) || (tc.prob != nil && *r.LanguageProbability != *tc.prob) {
				t.Fatalf("probability = %v, want %v", r.LanguageProbability, tc.prob)
			}
		})
	}
	if _, err := ParseVerboseJSON([]byte("<html>")); err == nil {
		t.Fatal("not JSON must fail")
	}
}

func ptr(f float64) *float64 { return &f }

func clientFor(ts *httptest.Server, timeout time.Duration) *Client {
	return &Client{BaseURL: func() string { return ts.URL }, Timeout: timeout}
}

func TestTranscribeSendsTheDocumentedFields(t *testing.T) {
	var fields map[string]string
	var path string
	var fileLen int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		mr, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		fields = map[string]string{}
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "file" {
				fileLen = len(b)
			} else {
				fields[p.FormName()] = string(b)
			}
		}
		_, _ = w.Write(testdataFile(t, "inference_verbose_json.sample.json"))
	}))
	defer ts.Close()
	res, err := clientFor(ts, 0).Transcribe(context.Background(), []byte("RIFFxxxx"), "es", "names: Ana")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "How do I clean a deep cut?" {
		t.Fatalf("text = %q", res.Text)
	}
	want := map[string]string{"response_format": "verbose_json", "language": "es", "temperature": "0.0",
		"temperature_inc": "0.2", "no_timestamps": "true", "prompt": "names: Ana"}
	if path != "POST /inference" || fileLen != 8 || len(fields) != len(want) {
		t.Fatalf("path %q file %d fields %v", path, fileLen, fields)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, fields[k], v)
		}
	}
}

func TestTranscribeOmitsAnEmptyPrompt(t *testing.T) {
	var hasPrompt bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		_, hasPrompt = r.MultipartForm.Value["prompt"]
		_, _ = w.Write(testdataFile(t, "inference_verbose_json.sample.json"))
	}))
	defer ts.Close()
	if _, err := clientFor(ts, 0).Transcribe(context.Background(), []byte("x"), "auto", ""); err != nil {
		t.Fatal(err)
	}
	if hasPrompt {
		t.Fatal("an empty prompt must not be sent")
	}
}

func TestTranscribeFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    error
	}{
		{"500 is down", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, 0, ErrServerDown},
		{"503 is down", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "loading", http.StatusServiceUnavailable) }, 0, ErrServerDown},
		{"garbage is down", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }, 0, ErrServerDown},
		{"hang is a timeout", func(w http.ResponseWriter, r *http.Request) { time.Sleep(400 * time.Millisecond) }, 50 * time.Millisecond, ErrInferenceTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()
			_, err := clientFor(ts, tc.timeout).Transcribe(context.Background(), []byte("x"), "auto", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTranscribeRefusedIsDown(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	c := &Client{BaseURL: func() string { return url }}
	if _, err := c.Transcribe(context.Background(), []byte("x"), "auto", ""); !errors.Is(err, ErrServerDown) {
		t.Fatalf("err = %v", err)
	}
}

func TestTranscribeCallerLeavingIsNotATimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(400 * time.Millisecond) }))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	if _, err := clientFor(ts, 0).Transcribe(ctx, []byte("x"), "auto", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
