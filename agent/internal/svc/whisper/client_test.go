package whisper

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func clientFor(url string) *Client { return &Client{BaseURL: func() string { return url }} }

func TestClientParsesTheRecordedVerboseJSON(t *testing.T) {
	body, err := os.ReadFile("testdata/inference_verbose_json.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		path, ctype string
		fields      map[string]string
		fileCT      string
		fileLen     int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.ctype = r.URL.Path, r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		got.fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			got.fields[k] = v[0]
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		b, _ := io.ReadAll(f)
		got.fileCT, got.fileLen = h.Header.Get("Content-Type"), len(b)
		w.Write(body)
	}))
	defer srv.Close()

	wav := EncodeWAV16(make([]int16, 100), WhisperRate)
	res, err := clientFor(srv.URL).Transcribe(context.Background(), wav, "auto", "names: Ana")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "How do I clean a deep cut?" || res.Language != "en" || res.Seconds != 3.2 {
		t.Errorf("result = %+v", res)
	}
	if res.Probability == nil || *res.Probability != 0.97 {
		t.Errorf("probability = %v", res.Probability)
	}
	if got.path != "/inference" || !strings.HasPrefix(got.ctype, "multipart/form-data") || got.fileLen != len(wav) || got.fileCT != "audio/wav" {
		t.Errorf("request = %+v", got)
	}
	want := map[string]string{"response_format": "verbose_json", "language": "auto", "temperature": "0.0", "temperature_inc": "0.2", "no_timestamps": "true", "prompt": "names: Ana"}
	for k, v := range want {
		if got.fields[k] != v {
			t.Errorf("field %s = %q, want %q", k, got.fields[k], v)
		}
	}
	// No prompt field when there is no prompt.
	got.fields = nil
	if _, err := clientFor(srv.URL).Transcribe(context.Background(), wav, "es", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.fields["prompt"]; ok || got.fields["language"] != "es" {
		t.Errorf("fields = %v", got.fields)
	}
}

func TestClientWithoutProbabilityOmitsIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"task":"transcribe","language":"spanish","duration":1.5,"text":" hola ","segments":[]}`)
	}))
	defer srv.Close()
	res, err := clientFor(srv.URL).Transcribe(context.Background(), []byte("x"), "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hola" || res.Language != "es" || res.Probability != nil {
		t.Errorf("result = %+v", res)
	}
}

func TestClientFailures(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	cases := []struct {
		name    string
		handler http.HandlerFunc
		kind    string
		reason  string
		status  int
	}{
		{"500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, KindDown, "status", 500},
		{"503", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, KindDown, "status", 503},
		{"400", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }, KindDown, "status", 400},
		{"not json", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>") }, KindDown, "bad_reply", 0},
		{"error field", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"error":"failed to process audio"}`) }, KindDown, "error_field", 0},
		{"hang", func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body) // the server notices a closed connection once the body is read
			select {
			case <-hang:
			case <-r.Context().Done():
			}
		}, KindTimeout, "timeout", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			cl := clientFor(srv.URL)
			cl.Timeout = 80 * time.Millisecond
			_, err := cl.Transcribe(context.Background(), []byte("x"), "auto", "")
			var ce *ClientError
			if !errors.As(err, &ce) || ce.Kind != c.kind || ce.Reason != c.reason || ce.Status != c.status {
				t.Fatalf("err = %v, want kind %s reason %s status %d", err, c.kind, c.reason, c.status)
			}
		})
	}
}

func TestClientServerDownAndCallerGone(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	_, err := clientFor(url).Transcribe(context.Background(), []byte("x"), "auto", "")
	var ce *ClientError
	if !errors.As(err, &ce) || ce.Kind != KindDown || ce.Reason != "connect" {
		t.Fatalf("err = %v", err)
	}
	if clientFor(url).Alive(context.Background()) {
		t.Error("a dead server is not alive")
	}

	// The caller leaving is its own error, not a timeout and not "down".
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer slow.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err = clientFor(slow.URL).Transcribe(ctx, []byte("x"), "auto", "")
	if !errors.Is(err, context.Canceled) || errors.As(err, &ce) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestClientAlive(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>whisper</html>") }))
	defer ok.Close()
	if !clientFor(ok.URL).Alive(context.Background()) {
		t.Error("200 on / is alive")
	}
}

func TestLanguageCode(t *testing.T) {
	for in, want := range map[string]string{"english": "en", "Spanish": "es", "es": "es", " EN ": "en", "haitian creole": "ht", "klingon": "klingon", "": ""} {
		if got := LanguageCode(in); got != want {
			t.Errorf("LanguageCode(%q) = %q, want %q", in, got, want)
		}
	}
	for _, ok := range []string{"auto", "en", "es", "haw"} {
		if !ValidLanguage(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, no := range []string{"", "EN", "e", "english", "en;rm", "auto2", "a1"} {
		if ValidLanguage(no) {
			t.Errorf("%q must be invalid", no)
		}
	}
}
