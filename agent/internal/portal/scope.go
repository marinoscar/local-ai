package portal

import (
	"math"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Rate-limit buckets of a Portal session (survival-portal.md section 3.3).
const (
	BucketLoad = "load"
	BucketSTT  = "stt"
	BucketTTS  = "tts"
	// BucketImport is POST /ark/import, BucketRestart the two service restarts (one budget for both).
	BucketImport  = "import"
	BucketRestart = "restart"
)

// Limit is N requests per Window.
type Limit struct {
	N      int
	Window time.Duration
}

// Limits are the per-session limits: model loads 6 per hour, STT 30 per minute, TTS 120 per minute,
// imports from the ark drive 3 per hour, service restarts 6 per hour.
var Limits = map[string]Limit{
	BucketLoad:    {N: 6, Window: time.Hour},
	BucketSTT:     {N: 30, Window: time.Minute},
	BucketTTS:     {N: 120, Window: time.Minute},
	BucketImport:  {N: 3, Window: time.Hour},
	BucketRestart: {N: 6, Window: time.Hour},
}

// limitCode is the doctor code of a 429 per bucket: speech routes LAI-SVC-015, model loads
// LAI-SVC-114, imports and restarts (the buttons of a fix) LAI-SVC-117.
var limitCode = map[string]string{
	BucketLoad: doctor.SVC114, BucketSTT: doctor.SVC015, BucketTTS: doctor.SVC015,
	BucketImport: doctor.SVC117, BucketRestart: doctor.SVC117,
}

// BucketOf names the rate-limit bucket of a route, "" for a route without a limit. The routes the
// Portal session reaches are few, so the table is explicit.
func BucketOf(method, pattern string) string {
	switch {
	case method == http.MethodPost && pattern == "/models/{id}/load":
		return BucketLoad
	case method == http.MethodPost && pattern == "/speech/stt":
		return BucketSTT
	case method == http.MethodPost && pattern == "/speech/tts":
		return BucketTTS
	case method == http.MethodPost && pattern == "/ark/import":
		return BucketImport
	case method == http.MethodPost && (pattern == "/services/kiwix/restart" || pattern == "/services/whisper/restart"):
		return BucketRestart
	}
	return ""
}

// Allow counts one request of session id in bucket. When the session is over its limit it returns
// false and the seconds until the oldest counted request leaves the window. Buckets live with the
// session and die with it.
func (s *Sessions) Allow(id, bucket string) (ok bool, retryAfter int) {
	lim, limited := Limits[bucket]
	if !limited {
		return true, 0
	}
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, found := s.byID[id]
	if !found {
		return true, 0 // the session ended meanwhile: the auth step answers for it
	}
	cut := now.Add(-lim.Window)
	hits := sess.hits[bucket][:0]
	for _, t := range sess.hits[bucket] {
		if t.After(cut) {
			hits = append(hits, t)
		}
	}
	if len(hits) >= lim.N {
		sess.hits[bucket] = hits
		wait := int(math.Ceil(hits[0].Add(lim.Window).Sub(now).Seconds()))
		if wait < 1 {
			wait = 1
		}
		return false, wait
	}
	sess.hits[bucket] = append(hits, now)
	return true, 0
}

// Guard is api.Options.Guard: it runs for a request authenticated as a Portal session, after the
// route's Auth list (the scope allowlist) and the Origin rule have accepted it. It enforces the rate
// limits: over the limit the answer is 429 with Retry-After, LAI-SVC-015 for speech, LAI-SVC-114
// for model loads and LAI-SVC-117 for imports and restarts. The load restrictions (loadable only,
// forced body) and the import default (forced body) are in the handlers themselves.
func (s *Sessions) Guard(w http.ResponseWriter, r *http.Request, rt *api.Route, p api.Principal) error {
	bucket := BucketOf(rt.Method, rt.Pattern)
	if bucket == "" {
		return nil
	}
	ok, wait := s.Allow(p.PortalSessionID, bucket)
	if ok {
		return nil
	}
	s.opt.Metrics.RateLimited(bucket)
	e := api.NewError(limitCode[bucket], http.StatusTooManyRequests, map[string]any{"bucket": bucket, "retry_after_s": wait})
	e.RetryAfter = wait
	return e
}
