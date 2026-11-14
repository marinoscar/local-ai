package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// The song store: /var/lib/localai/music/songs/<song-id>/ with audio.<ext> and meta.json (E11 contract
// section 1). A song appears atomically: it is assembled in songs/.tmp-<id>/ and renamed into place,
// so a reader never sees half a song and a crash leaves only a .tmp- directory, which the next start
// removes.

// Canonical paths (overview section 3).
const (
	DefaultSongsDir = "/var/lib/localai/music/songs"
	DefaultWorkDir  = "/var/lib/localai/music/work"
	RuntimeDir      = "/opt/localai/acestep/current"
)

const (
	metaFile  = "meta.json"
	tmpPrefix = ".tmp-"
)

// Origins of a song.
const (
	OriginLocal = "local"
	OriginCloud = "cloud"
)

var songIDRE = regexp.MustCompile(`^sng_[a-z2-7]{12}$`)

// ValidSongID reports whether id has the shape of a song id (and so is safe in a path).
func ValidSongID(id string) bool { return songIDRE.MatchString(id) }

// ErrSongNotFound is an unknown song id.
var ErrSongNotFound = errors.New("music: no such song")

// Meta is meta.json: the inputs of the song minus the sample bytes, and what came out. The lyrics are
// kept with the song (it is the owner's data on the owner's disk); they never reach a log.
type Meta struct {
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"created_at"`
	OpID      string      `json:"op_id"`
	Variation int         `json:"variation"`
	Lyrics    string      `json:"lyrics"`
	Tags      string      `json:"tags"`
	Language  string      `json:"language"`
	DurationS float64     `json:"duration_s"`
	Requested int         `json:"requested_duration_s"`
	ModelID   string      `json:"model_id"`
	Seed      *int64      `json:"seed"`
	Mime      string      `json:"mime"`
	Audio     string      `json:"audio"`
	SizeBytes int64       `json:"size_bytes"`
	Origin    string      `json:"origin"`
	RequestID string      `json:"request_id,omitempty"`
	Sample    *SampleMeta `json:"sample"`
}

// SampleMeta describes the reference sample a song was made with (never its bytes).
type SampleMeta struct {
	Mode      string  `json:"mode"`
	Strength  float64 `json:"strength"`
	Mime      string  `json:"mime"`
	SizeBytes int64   `json:"size_bytes"`
	Seconds   float64 `json:"duration_s"`
}

// Song is one entry of GET /music/songs.
type Song struct {
	ID        string  `json:"id"`
	CreatedAt string  `json:"created_at"`
	Title     string  `json:"title"`
	Tags      string  `json:"tags"`
	Language  string  `json:"language"`
	DurationS float64 `json:"duration_s"`
	ModelID   string  `json:"model_id"`
	Seed      *int64  `json:"seed"`
	Mime      string  `json:"mime"`
	SizeBytes int64   `json:"size_bytes"`
	Origin    string  `json:"origin"`
}

// SongList is GET /music/songs.
type SongList struct {
	Songs      []Song  `json:"songs"`
	NextCursor *string `json:"next_cursor"`
}

// Store is the song directory.
type Store struct {
	Dir string
}

func (s Store) dir(id string) string { return filepath.Join(s.Dir, id) }

// Sweep removes the half-written songs a crash left (songs/.tmp-*).
func (s Store) Sweep() {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			_ = os.RemoveAll(filepath.Join(s.Dir, e.Name()))
		}
	}
}

// Add moves the audio file src into a new song directory and writes meta.json. meta.ID, Mime and
// the rest are the caller's; Audio and SizeBytes are filled here. src is renamed when it is on the
// same filesystem and copied otherwise; it is gone after a successful Add.
func (s Store) Add(src string, ext string, meta Meta) (Meta, error) {
	if !ValidSongID(meta.ID) {
		return Meta{}, fmt.Errorf("music: bad song id")
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return Meta{}, err
	}
	tmp := filepath.Join(s.Dir, tmpPrefix+meta.ID)
	_ = os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0o750); err != nil {
		return Meta{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	meta.Audio = "audio." + ext
	dst := filepath.Join(tmp, meta.Audio)
	if err := moveFile(src, dst); err != nil {
		return Meta{}, err
	}
	st, err := os.Stat(dst)
	if err != nil {
		return Meta{}, err
	}
	meta.SizeBytes = st.Size()
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, err
	}
	if err := writeFileSync(filepath.Join(tmp, metaFile), raw, 0o640); err != nil {
		return Meta{}, err
	}
	if err := os.Rename(tmp, s.dir(meta.ID)); err != nil {
		return Meta{}, err
	}
	ok = true
	return meta, nil
}

// Meta reads the meta.json of a song.
func (s Store) Meta(id string) (Meta, error) {
	if !ValidSongID(id) {
		return Meta{}, ErrSongNotFound
	}
	raw, err := os.ReadFile(filepath.Join(s.dir(id), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, ErrSongNotFound
	}
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil || m.ID != id || !validAudioName(m.Audio) {
		return Meta{}, fmt.Errorf("music: the meta.json of a song is not valid")
	}
	return m, nil
}

func validAudioName(n string) bool {
	switch n {
	case "audio.wav", "audio.mp3", "audio.flac", "audio.ogg":
		return true
	}
	return false
}

// AudioPath is the path of a song's audio file.
func (s Store) AudioPath(m Meta) string { return filepath.Join(s.dir(m.ID), m.Audio) }

// Delete removes a song; ErrSongNotFound when there is none.
func (s Store) Delete(id string) error {
	if !ValidSongID(id) {
		return ErrSongNotFound
	}
	d := s.dir(id)
	if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
		return ErrSongNotFound
	}
	return os.RemoveAll(d)
}

// All lists every song, newest first (by creation time, then id).
func (s Store) All() ([]Meta, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range ents {
		if !e.IsDir() || !ValidSongID(e.Name()) {
			continue
		}
		m, err := s.Meta(e.Name())
		if err != nil {
			continue // a broken entry is skipped, not fatal to the list
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// errBadCursor is a cursor that names no song.
var errBadCursor = errors.New("music: unknown cursor")

// Page returns up to limit songs after the song cursor names ("" is the start).
func (s Store) Page(limit int, cursor string) (SongList, error) {
	all, err := s.All()
	if err != nil {
		return SongList{}, err
	}
	start := 0
	if cursor != "" {
		start = -1
		for i, m := range all {
			if m.ID == cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return SongList{}, errBadCursor
		}
	}
	out := SongList{Songs: []Song{}}
	for i := start; i < len(all) && len(out.Songs) < limit; i++ {
		out.Songs = append(out.Songs, all[i].Song())
	}
	if start+len(out.Songs) < len(all) && len(out.Songs) > 0 {
		next := out.Songs[len(out.Songs)-1].ID
		out.NextCursor = &next
	}
	return out, nil
}

// Song is the list view of m.
func (m Meta) Song() Song {
	return Song{
		ID: m.ID, CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339), Title: Title(m.Lyrics, m.Tags),
		Tags: m.Tags, Language: m.Language, DurationS: m.DurationS, ModelID: m.ModelID, Seed: m.Seed,
		Mime: m.Mime, SizeBytes: m.SizeBytes, Origin: m.Origin,
	}
}

// maxTitle is the longest title, in characters.
const maxTitle = 80

// Title is the first lyric line that is not a section tag ("[verse]"), else the tags, else "Song".
func Title(lyrics, tags string) string {
	for _, line := range strings.Split(lyrics, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")) {
			continue
		}
		return clip(line, maxTitle)
	}
	if t := strings.TrimSpace(tags); t != "" {
		return clip(t, maxTitle)
	}
	return "Song"
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// moveFile renames src to dst, or copies and removes it across filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func writeFileSync(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
