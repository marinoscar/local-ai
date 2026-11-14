package music_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/music"
)

func TestGenerateRequests(t *testing.T) {
	type tc struct {
		name   string
		setup  func(r *rig)
		form   form
		status int
		code   string
		reason string
		check  func(t *testing.T, r *rig, acc music.Accepted)
	}
	cases := []tc{
		{
			name: "happy path without a sample", form: form{fields: baseFields(nil)}, status: http.StatusAccepted,
			setup: func(r *rig) { r.shim.AutoComplete = true },
			check: func(t *testing.T, r *rig, acc music.Accepted) {
				v := r.waitOp(acc.OpID)
				if v.State != music.StateDone || len(v.SongIDs) != 2 || v.Error != nil {
					t.Fatalf("op = %+v", v)
				}
				req := r.shim.Requests[0]
				if req.SamplePath != nil || req.Variations != 2 || req.DurationS != 30 || req.Language != "es" || *req.Seed != 7 {
					t.Fatalf("task = %+v", req)
				}
				m, _, err := r.svc.Song(v.SongIDs[1])
				if err != nil {
					t.Fatal(err)
				}
				if m.Origin != music.OriginLocal || *m.Seed != 8 || m.Variation != 1 || m.Mime != "audio/wav" || m.Lyrics != secretLyrics || m.Sample != nil ||
					len(m.SHA256) != 64 || m.CloudRequestID != nil || m.Title != "Zebra-marker walks the river" {
					t.Fatalf("meta = %+v", m)
				}
				if got := r.workEntries(); len(got) != 0 {
					t.Fatalf("the work directory keeps %v", got)
				}
			},
		},
		{
			name: "happy path with a sounds_like sample", form: form{fields: baseFields(map[string]string{"variations": "1"}), sample: wavBytes(5)},
			status: http.StatusAccepted, setup: func(r *rig) { r.shim.AutoComplete = true },
			check: func(t *testing.T, r *rig, acc music.Accepted) {
				v := r.waitOp(acc.OpID)
				if v.State != music.StateDone || len(v.SongIDs) != 1 {
					t.Fatalf("op = %+v", v)
				}
				req := r.shim.Requests[0]
				if req.SamplePath == nil || *req.SampleMode != music.SampleSoundsLike || *req.Strength != 0.5 || !strings.HasSuffix(*req.SamplePath, "sample.wav") {
					t.Fatalf("task = %+v", req)
				}
				m, _, _ := r.svc.Song(v.SongIDs[0])
				if m.Sample == nil || m.Sample.Mime != "audio/wav" || m.Sample.Seconds != 5 || len(m.Sample.SHA256) != 64 {
					t.Fatalf("meta.sample = %+v", m.Sample)
				}
				if got := r.workEntries(); len(got) != 0 {
					t.Fatalf("the sample was left behind: %v", got)
				}
			},
		},
		{
			name: "same_song defaults to strength 1", form: form{fields: baseFields(map[string]string{"sample_mode": "same_song"}), sample: flacBytes(44100, 44100*20)},
			status: http.StatusAccepted,
			check: func(t *testing.T, r *rig, acc music.Accepted) {
				req := r.shim.Requests[0]
				if *req.SampleMode != music.SampleSameSong || *req.Strength != 1 || !strings.HasSuffix(*req.SamplePath, "sample.flac") {
					t.Fatalf("task = %+v", req)
				}
			},
		},
		{
			name: "a seed is drawn when none is given", form: form{fields: baseFields(map[string]string{"seed": "-"})}, status: http.StatusAccepted,
			check: func(t *testing.T, r *rig, acc music.Accepted) {
				if s := r.shim.Requests[0].Seed; s == nil || *s < 0 || *s > music.MaxSeed {
					t.Fatalf("seed %v", s)
				}
			},
		},
		{
			name: "fewer songs than asked", form: form{fields: baseFields(nil)}, status: http.StatusAccepted,
			setup: func(r *rig) { r.shim.AutoComplete, r.shim.Outputs = true, 1 },
			check: func(t *testing.T, r *rig, acc music.Accepted) {
				v := r.waitOp(acc.OpID)
				if v.State != music.StateDone || len(v.SongIDs) != 1 || v.Error == nil || v.Error.Code != doctor.SVC152 {
					t.Fatalf("op = %+v", v)
				}
			},
		},
		{
			name: "the engine rejects the task", form: form{fields: baseFields(nil)}, status: 400, code: doctor.SVC154, reason: "language es is not sung",
			setup: func(r *rig) { r.shim.Reject = "language es is not sung" },
		},
		{
			name: "music not loaded", form: form{fields: baseFields(nil)}, status: http.StatusConflict, code: doctor.SVC151,
			setup: func(r *rig) { r.shim.Status = music.HealthLoading },
		},
		{
			name: "music engine stopped", form: form{fields: baseFields(nil), sample: wavBytes(2)}, status: http.StatusConflict, code: doctor.SVC151,
			setup: func(r *rig) { r.shim.Kill() },
		},
		{
			name: "the engine's own queue is full", form: form{fields: baseFields(nil)}, status: http.StatusTooManyRequests, code: doctor.SVC153,
			setup: func(r *rig) {
				r.shim.MaxQueue = 1
				_, _ = r.shim.Submit(context.Background(), music.TaskRequest{OutputDir: filepath.Join(r.work, "x")})
			},
		},
		{
			name: "disk full", form: form{fields: baseFields(nil)}, status: http.StatusInsufficientStorage, code: doctor.SVC155,
			setup: func(r *rig) { r.setFree(2 << 20) },
		},
		{name: "unsupported sample type", form: form{fields: baseFields(nil), sample: []byte("just some text, not audio")}, status: 400, code: doctor.SVC154, reason: music.ReasonUnsupportedType},
		{name: "sample too long", form: form{fields: baseFields(nil), sample: wavBytes(61)}, status: 400, code: doctor.SVC154, reason: music.ReasonTooLong},
		{name: "sample without a length", form: form{fields: baseFields(nil), sample: flacBytes(44100, 0)}, status: 400, code: doctor.SVC154, reason: music.ReasonUnknownDuration},
		{name: "empty sample", form: form{fields: baseFields(nil), sample: []byte{}}, status: 400, code: doctor.SVC154, reason: music.ReasonEmpty},
		{name: "lyrics too long", form: form{fields: baseFields(map[string]string{"lyrics": strings.Repeat("a", 8193)})}, status: 400, code: doctor.SVC154, reason: "too_long"},
		{name: "tags too long", form: form{fields: baseFields(map[string]string{"tags": strings.Repeat("a", 513)})}, status: 400, code: doctor.SVC154, reason: "too_long"},
		{name: "nothing to sing", form: form{fields: baseFields(map[string]string{"lyrics": " ", "tags": "-"})}, status: 400, code: doctor.SVC154, reason: "empty"},
		{name: "duration out of range", form: form{fields: baseFields(map[string]string{"duration_s": "241"})}, status: 400, code: doctor.SVC154, reason: "out_of_range"},
		{name: "duration not an integer", form: form{fields: baseFields(map[string]string{"duration_s": "1e2"})}, status: 400, code: doctor.SVC154, reason: "not_an_integer"},
		{name: "five variations", form: form{fields: baseFields(map[string]string{"variations": "5"})}, status: 400, code: doctor.SVC154, reason: "out_of_range"},
		{name: "bad language", form: form{fields: baseFields(map[string]string{"language": "Spanish"})}, status: 400, code: doctor.SVC154, reason: "invalid"},
		{name: "seed over 2^31-1", form: form{fields: baseFields(map[string]string{"seed": "2147483648"})}, status: 400, code: doctor.SVC154, reason: "out_of_range"},
		{name: "negative seed", form: form{fields: baseFields(map[string]string{"seed": "-1"})}, status: 400, code: doctor.SVC154, reason: "out_of_range"},
		{name: "strength without a sample", form: form{fields: baseFields(map[string]string{"strength": "0.3"})}, status: 400, code: doctor.SVC154, reason: "needs_sample"},
		{name: "strength out of range", form: form{fields: baseFields(map[string]string{"strength": "1.5"}), sample: wavBytes(1)}, status: 400, code: doctor.SVC154, reason: "out_of_range"},
		{name: "bad sample mode", form: form{fields: baseFields(map[string]string{"sample_mode": "remix"}), sample: wavBytes(1)}, status: 400, code: doctor.SVC154, reason: "invalid"},
		{name: "unknown field", form: form{fields: baseFields(map[string]string{"tempo": "120"})}, status: 400, code: doctor.SVC154, reason: "unknown_field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.setup != nil {
				c.setup(r)
			}
			rec := r.generate(c.form)
			if c.code != "" {
				e := expectError(t, rec, c.status, c.code)
				if c.reason != "" && e.Error.Details["reason"] != c.reason {
					t.Fatalf("reason %v, want %s", e.Error.Details["reason"], c.reason)
				}
				if got := r.workEntries(); len(got) != 0 {
					t.Fatalf("a refused request left files: %v", got)
				}
				if len(r.songDirs()) != 0 {
					t.Fatal("a refused request made a song")
				}
			} else if rec.Code != c.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if c.check != nil {
				acc := decode[music.Accepted](t, rec)
				if rec.Header().Get("X-LocalAI-Op-Id") != acc.OpID || !strings.HasPrefix(acc.OpID, "op_") {
					t.Fatalf("op id header %q, body %q", rec.Header().Get("X-LocalAI-Op-Id"), acc.OpID)
				}
				c.check(t, r, acc)
			}
			r.noSecretsInLogs()
		})
	}
}

func TestBodyTooLarge(t *testing.T) {
	r := newRig(t)
	rec := r.generate(form{fields: baseFields(nil), sample: append(wavBytes(1), bytes.Repeat([]byte{0}, music.MaxSampleBytes)...)})
	expectError(t, rec, http.StatusRequestEntityTooLarge, doctor.SVC154)
	if got := r.workEntries(); len(got) != 0 {
		t.Fatalf("the partial sample was kept: %v", got)
	}
}

func TestLocalQueueFull(t *testing.T) {
	r := newRig(t, func(d *music.Deps) { d.MaxQueue = 1 })
	first := r.generate(form{fields: baseFields(nil)})
	if first.Code != http.StatusAccepted {
		t.Fatal(first.Body.String())
	}
	e := expectError(t, r.generate(form{fields: baseFields(nil)}), http.StatusTooManyRequests, doctor.SVC153)
	_ = e
	st := r.svc.Status()
	if st.Queue.Queued != 1 || st.Queue.Max != 1 || st.EngineState != music.EngineReady || st.LoadedModel == nil || *st.LoadedModel != "music-example" || !st.Installed {
		t.Fatalf("status = %+v", st)
	}
	rec := r.do(call{target: "/music/status"})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
}

func TestCancel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		down    bool
	}{
		{"queued", false, false},
		{"running", true, false},
		{"engine gone", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			acc := decode[music.Accepted](t, r.generate(form{fields: baseFields(nil), sample: wavBytes(1)}))
			if tc.running {
				r.shim.Progress(r.shim.Last(), 0.3)
				r.waitState(acc.OpID, music.StateRunning)
			}
			if tc.down {
				r.shim.Kill()
			}
			rec := r.do(call{method: http.MethodPost, target: "/music/ops/" + acc.OpID + "/cancel", body: strings.NewReader("{}")})
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			v := r.waitOp(acc.OpID)
			if v.State != music.StateCancelled || v.Error == nil || v.Error.Code != doctor.SVC158 || len(v.SongIDs) != 0 {
				t.Fatalf("op = %+v", v)
			}
			if !tc.down && len(r.shim.Cancelled) != 1 {
				t.Fatal("the engine was not told")
			}
			r.svc.Close()
			if len(r.songDirs()) != 0 || len(r.workEntries()) != 0 {
				t.Fatalf("files left: songs %v work %v", r.songDirs(), r.workEntries())
			}
			// A second cancel answers the same state.
			again := r.do(call{method: http.MethodPost, target: "/music/ops/" + acc.OpID + "/cancel", body: strings.NewReader("{}")})
			if decode[music.OpView](t, again).State != music.StateCancelled {
				t.Fatal(again.Body.String())
			}
		})
	}
	r := newRig(t)
	expectError(t, r.do(call{method: http.MethodPost, target: "/music/ops/op_unknown/cancel", body: strings.NewReader("{}")}), 404, "LAI-AGT-021")
	expectError(t, r.do(call{target: "/music/ops/op_unknown"}), 404, "LAI-AGT-021")
}

func TestEngineFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(r *rig)
		code string
	}{
		{"the engine dies mid-song", func(r *rig) { r.shim.Progress(r.shim.Last(), 0.5); r.shim.Kill() }, doctor.SVC152},
		{"the engine reports a failure", func(r *rig) { r.shim.Fail(r.shim.Last()) }, doctor.SVC152},
		{"the disk fills before the song is saved", func(r *rig) { r.setFree(1 << 20); _ = r.shim.Complete(r.shim.Last()) }, doctor.SVC155},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			acc := decode[music.Accepted](t, r.generate(form{fields: baseFields(nil), sample: wavBytes(1)}))
			tc.act(r)
			v := r.waitOp(acc.OpID)
			if v.State != music.StateFailed || v.Error == nil || v.Error.Code != tc.code || len(v.SongIDs) != 0 {
				t.Fatalf("op = %+v", v)
			}
			view := r.do(call{target: "/music/ops/" + acc.OpID})
			if view.Code != 200 {
				t.Fatal(view.Body.String())
			}
			r.svc.Close()
			if len(r.songDirs()) != 0 || len(r.workEntries()) != 0 {
				t.Fatalf("orphan files: songs %v work %v", r.songDirs(), r.workEntries())
			}
			evs := r.pub.all()
			last := evs[len(evs)-1]
			if last.typ != events.TypeMusicDone || last.data.Code != tc.code || last.data.State != music.StateFailed {
				t.Fatalf("last event = %+v", last)
			}
			r.noSecretsInLogs()
		})
	}
}

func TestEventsAndNoLyrics(t *testing.T) {
	r := newRig(t)
	acc := decode[music.Accepted](t, r.generate(form{fields: baseFields(nil)}))
	id := r.shim.Last()
	r.shim.Progress(id, 0.5)
	r.waitState(acc.OpID, music.StateRunning)
	if err := r.shim.Complete(id); err != nil {
		t.Fatal(err)
	}
	v := r.waitOp(acc.OpID)
	if v.State != music.StateDone {
		t.Fatalf("op = %+v", v)
	}
	evs := r.pub.all()
	if evs[0].typ != events.TypeMusicProgress || evs[0].data.State != music.StateQueued {
		t.Fatalf("first event = %+v", evs[0])
	}
	sawRunning := false
	for _, e := range evs {
		if e.typ == events.TypeMusicProgress && e.data.State == music.StateRunning && e.data.Progress != nil && *e.data.Progress == 0.5 {
			sawRunning = true
		}
		raw, _ := json.Marshal(e.data)
		if strings.Contains(string(raw), "Zebra") {
			t.Fatalf("an event carries the lyrics: %s", raw)
		}
	}
	last := evs[len(evs)-1]
	if !sawRunning || last.typ != events.TypeMusicDone || len(last.data.SongIDs) != 2 || last.data.SongIDs[0] != acc.SongIDs[0] {
		t.Fatalf("events = %+v", evs)
	}
	r.noSecretsInLogs()
	if !strings.Contains(r.logs.String(), `"lyrics_bytes":`) {
		t.Fatalf("the request line is missing:\n%s", r.logs.String())
	}
}

func TestSongsListAudioAndDelete(t *testing.T) {
	r := newRig(t)
	r.shim.AutoComplete = true
	var ids []string
	for i := 0; i < 2; i++ {
		acc := decode[music.Accepted](t, r.generate(form{fields: baseFields(nil)}))
		ids = append(ids, r.waitOp(acc.OpID).SongIDs...)
	}
	if len(ids) != 4 {
		t.Fatalf("songs %v", ids)
	}

	page := decode[music.SongList](t, r.do(call{target: "/music/songs?limit=3"}))
	if len(page.Songs) != 3 || page.NextCursor == nil {
		t.Fatalf("page 1 = %+v", page)
	}
	if s := page.Songs[0]; s.Title != "Zebra-marker walks the river" || s.Origin != "local" || s.Mime != "audio/wav" || s.SizeBytes == 0 {
		t.Fatalf("song = %+v", s)
	}
	page2 := decode[music.SongList](t, r.do(call{target: "/music/songs?limit=3&cursor=" + *page.NextCursor}))
	if len(page2.Songs) != 1 || page2.NextCursor != nil {
		t.Fatalf("page 2 = %+v", page2)
	}
	expectError(t, r.do(call{target: "/music/songs?cursor=sng_aaaaaaaaaaaa"}), 400, "LAI-AGT-020")
	expectError(t, r.do(call{target: "/music/songs?limit=0"}), 400, "LAI-AGT-020")

	// Audio, whole and by range.
	whole := r.do(call{target: "/music/songs/" + ids[0] + "/audio"})
	if whole.Code != 200 || whole.Header().Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(whole.Body.Bytes(), []byte("RIFF")) {
		t.Fatalf("audio: %d %v", whole.Code, whole.Header())
	}
	dl := r.do(call{target: "/music/songs/" + ids[0] + "/audio?download=1"})
	if !strings.HasPrefix(dl.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %v", dl.Header())
	}
	part := r.do(call{target: "/music/songs/" + ids[0] + "/audio", header: map[string]string{"Range": "bytes=0-3"}})
	if part.Code != http.StatusPartialContent || part.Body.String() != "RIFF" || !strings.HasPrefix(part.Header().Get("Content-Range"), "bytes 0-3/") {
		t.Fatalf("range: %d %q %v", part.Code, part.Body.String(), part.Header())
	}
	expectError(t, r.do(call{target: "/music/songs/sng_aaaaaaaaaaaa/audio"}), 404, "LAI-AGT-021")
	expectError(t, r.do(call{target: "/music/songs/..%2f..%2fetc/audio"}), 404, "LAI-AGT-021")

	// Delete needs admin, like DELETE /models/{id}.
	del := call{method: http.MethodDelete, target: "/music/songs/" + ids[0]}
	expectError(t, r.do(del), http.StatusForbidden, "LAI-SEC-002")
	if _, err := os.Stat(filepath.Join(r.songs, ids[0])); err != nil {
		t.Fatal("an operator deleted a song")
	}
	del.principal = "admin"
	rec := r.do(del)
	if got := decode[music.DeletedSong](t, rec); rec.Code != 200 || !got.Deleted || got.ID != ids[0] || got.Bytes == 0 {
		t.Fatal(rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(r.songs, ids[0])); !os.IsNotExist(err) {
		t.Fatal("the song is still there")
	}
	again := r.do(del)
	if again.Code != 200 || decode[music.DeletedSong](t, again) != (music.DeletedSong{ID: ids[0], Deleted: false}) {
		t.Fatal(again.Body.String())
	}
	var deletes []string
	for _, ev := range r.audit.evs {
		if ev.Action == music.ActionDelete {
			deletes = append(deletes, ev.Target["song"]+"/"+ev.Target["bytes"])
		}
	}
	if len(deletes) != 2 || !strings.HasPrefix(deletes[0], ids[0]+"/") || deletes[0] == ids[0]+"/" || deletes[1] != ids[0]+"/" {
		t.Fatalf("delete audit = %v", deletes)
	}
}

func TestStartupSweepsLeftovers(t *testing.T) {
	dir := t.TempDir()
	songs, work := filepath.Join(dir, "songs"), filepath.Join(dir, "work")
	for _, p := range []string{filepath.Join(songs, ".tmp-sng_aaaaaaaaaaaa"), filepath.Join(work, "op_aaaaaaaaaaaa", "out"), filepath.Join(work, "incoming")} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := music.New(music.Deps{Shim: nil})
	if err == nil || svc != nil {
		t.Fatal("a service without a shim must be refused")
	}
	r := newRig(t, func(d *music.Deps) { d.SongsDir, d.WorkDir = songs, work })
	_ = r
	if ents, _ := os.ReadDir(songs); len(ents) != 0 {
		t.Fatalf("songs: %v", ents)
	}
	if ents, _ := os.ReadDir(work); len(ents) != 0 {
		t.Fatalf("work: %v", ents)
	}
}

func TestTitle(t *testing.T) {
	for _, c := range []struct{ lyrics, tags, want string }{
		{"[verse]\n\n  First line  \nsecond", "pop", "First line"},
		{"[intro]\n[outro]", "latin pop", "latin pop"},
		{"", "", "Song"},
		{strings.Repeat("a", 100), "", strings.Repeat("a", 79) + "…"},
	} {
		if got := music.Title(c.lyrics, c.tags); got != c.want {
			t.Errorf("Title(%q, %q) = %q, want %q", c.lyrics, c.tags, got, c.want)
		}
	}
}
