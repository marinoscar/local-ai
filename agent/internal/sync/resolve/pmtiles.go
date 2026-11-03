package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/subproc"
)

// PMTilesTool is where the installer puts the go-pmtiles release binary (ark-sync.md section 4.9.3,
// rebuild-kit.md "Contents"). A missing tool is LAI-SYN-061.
const PMTilesTool = "/opt/localai/pmtiles/current/pmtiles"

// Codes and bounds of the pmtiles_extract resolver.
const (
	// CodePMTilesVerify: the extract failed `pmtiles verify`, or its header is not a vector map of the
	// requested area (LAI-SYN-023). The item is quarantined and rebuilt on the next run.
	CodePMTilesVerify = doctor.SYN023

	// pmtilesExclusive is the engine resource that serializes extracts: at most one at a time.
	pmtilesExclusive = "pmtiles"
	// pmtilesProbeDays is how many days back the newest planet build is looked for (k = 0..30).
	pmtilesProbeDays = 30
	// pmtilesAttempts is the number of extract attempts per run (ark-sync.md section 4.9.3).
	pmtilesAttempts = 2
	// pmtilesBBoxTolerance is how far the header's bounding box may fall short of the catalog's.
	pmtilesBBoxTolerance = 0.01

	pmtilesExtractTimeout = 6 * time.Hour
	pmtilesVerifyTimeout  = 2 * time.Hour
	pmtilesQuickTimeout   = 5 * time.Minute
	pmtilesRetryDelay     = 30 * time.Second
)

// PMTiles is the pmtiles_extract resolver (ark-sync.md section 4.9.3): a regional extract of the
// newest Protomaps planet build, made by the `pmtiles` CLI.
//
// Like Kiwix it has a planner half (Resolve: the newest build date and an estimate of the size) and an
// engine half (ResolveItem: a directory item whose tree is made by Build, one extract at a time). It
// writes only into the staged tree it is given, and its checks (`pmtiles verify`, the header) run as a
// content validator of the item machine (RegisterPMTilesValidator) so a bad extract is quarantined with
// LAI-SYN-023 rather than failed.
type PMTiles struct {
	// Env supplies Runner, SyncHome (HOME of the tool), Online, Bucket and the proxy. Required.
	Env *Env
	// HTTP and Version feed the metadata client (see Kiwix.HTTP).
	HTTP    *http.Client
	Version string
	// Tool is the pmtiles binary; default PMTilesTool.
	Tool string
	// Credential runs the tool as the service user (nil: as the agent, for a development run). The
	// staged tree is lent to that user while the tool runs and handed back afterwards.
	Credential *execx.Credential
	// ExtractTimeout bounds one extract attempt; default 6 hours.
	ExtractTimeout time.Duration
	// RetryDelay is the pause between the two attempts; default 30 seconds.
	RetryDelay time.Duration

	// Seams, nil means the real thing.
	Stat  func(string) (fs.FileInfo, error)
	Chown func(path string, uid, gid int) error
	Chmod func(path string, mode os.FileMode) error
	Euid  func() int
	Egid  func() int
	Sleep func(ctx context.Context, d time.Duration) error

	// expect maps an item id to what the catalog asked of it (pmtilesExpect), set by ResolveItem and
	// read by the validator of the same process.
	expect sync.Map

	once sync.Once
	ro   *readOnlyHTTP
}

// SourceType implements NamedResolver.
func (*PMTiles) SourceType() string { return "pmtiles_extract" }

func (p *PMTiles) web() *readOnlyHTTP {
	p.once.Do(func() { p.ro = &readOnlyHTTP{Client: p.HTTP, Version: p.Version} })
	return p.ro
}

func (p *PMTiles) tool() string {
	if p.Tool != "" {
		return p.Tool
	}
	return PMTilesTool
}

// checkTool fails with LAI-SYN-061 when the tool is missing or not executable.
func (p *PMTiles) checkTool() (string, error) {
	stat := p.Stat
	if stat == nil {
		stat = os.Stat
	}
	bin := p.tool()
	st, err := stat(bin)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return "", fail(CodeTool, nil, "pmtiles is not installed at %s", bin)
	}
	return bin, nil
}

// pmtilesExpect is what the catalog asked of one item: the validator compares the extract with it.
type pmtilesExpect struct {
	id       string
	planet   string // the planet URL of the build
	host     string
	bbox     [4]float64 // minLon, minLat, maxLon, maxLat
	maxZoom  int
	date     string
	fileName string // "<id>.pmtiles"
}

func (e pmtilesExpect) bboxArg() string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return f(e.bbox[0]) + "," + f(e.bbox[1]) + "," + f(e.bbox[2]) + "," + f(e.bbox[3])
}

// pmtilesSource is a validated pmtiles_extract catalog entry.
type pmtilesSource struct {
	id        string
	pattern   string
	host      string
	bbox      [4]float64
	maxZoom   int
	expected  int64
	catalogIt plan.CatalogItem
}

// source validates the catalog fields. The pattern must be an https URL with a host, no credentials and
// exactly one {date}; the bbox four finite numbers in range with min < max; the zoom 0 to 24. Nothing
// else ever becomes part of an argv.
func (p *PMTiles) source(it plan.CatalogItem) (pmtilesSource, error) {
	if it.Content == nil || it.Content.Source.Type != "pmtiles_extract" {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s is not a pmtiles_extract item", it.ID)
	}
	src := it.Content.Source
	s := pmtilesSource{id: it.ID, pattern: src.PlanetURLPattern, expected: it.Content.ExpectedSizeBytes, catalogIt: it}
	if !manifest.SafeName(it.ID) || strings.ContainsAny(it.ID, " \t") || strings.HasPrefix(it.ID, "-") {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: the id is not usable as a file name", it.ID)
	}
	if strings.Count(s.pattern, "{date}") != 1 {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: planet_url_pattern needs exactly one {date}", it.ID)
	}
	probe, err := url.Parse(strings.Replace(s.pattern, "{date}", "20000101", 1))
	switch {
	case err != nil || probe.Hostname() == "":
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s has no usable planet_url_pattern", it.ID)
	case probe.Scheme != "https":
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: planet_url_pattern must be https", it.ID)
	case probe.User != nil, probe.RawQuery != "", probe.Fragment != "":
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: planet_url_pattern carries credentials, a query or a fragment", it.ID)
	}
	s.host = strings.ToLower(probe.Hostname())
	if len(src.BBox) != 4 {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: bbox needs four numbers", it.ID)
	}
	copy(s.bbox[:], src.BBox)
	b := s.bbox
	for _, v := range b {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return pmtilesSource{}, fail(CodeRefused, nil, "item %s: bbox has a number that is not finite", it.ID)
		}
	}
	if b[0] >= b[2] || b[1] >= b[3] || b[0] < -180 || b[2] > 180 || b[1] < -90 || b[3] > 90 {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: bbox is empty or outside the world", it.ID)
	}
	if src.MaxZoom == nil || *src.MaxZoom < 0 || *src.MaxZoom > 24 {
		return pmtilesSource{}, fail(CodeRefused, nil, "item %s: maxzoom must be between 0 and 24", it.ID)
	}
	s.maxZoom = *src.MaxZoom
	return s, nil
}

func (s pmtilesSource) expect(date string) pmtilesExpect {
	return pmtilesExpect{
		id: s.id, planet: strings.Replace(s.pattern, "{date}", date, 1), host: s.host, bbox: s.bbox, maxZoom: s.maxZoom,
		date: date, fileName: s.id + ".pmtiles",
	}
}

// ---- the newest build ---------------------------------------------------------------------------------

// newest looks for the newest planet build: for k = 0..30 the date of today (UTC) minus k days is tried
// with a HEAD on the pattern until a 200 arrives. A transport error ends the search at once (the
// network is the problem, not the date), a status other than 200 goes to the next day. None: LAI-SYN-033.
func (p *PMTiles) newest(ctx context.Context, s pmtilesSource) (string, error) {
	today := p.Env.now().UTC()
	for k := 0; k <= pmtilesProbeDays; k++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		date := today.AddDate(0, 0, -k).Format("20060102")
		r, err := p.web().do(ctx, http.MethodHead, strings.Replace(s.pattern, "{date}", date, 1), true, 0, nil)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fail(CodeNoVersion, err, "cannot reach the planet builds at %s", s.host)
		}
		if r.Status == http.StatusOK {
			return date, nil
		}
	}
	return "", fail(CodeNoVersion, nil, "no planet build in the last %d days at %s", pmtilesProbeDays+1, s.host)
}

// ---- the planner's half -------------------------------------------------------------------------------

var pmtilesSizeRe = regexp.MustCompile(`(?i)(\d[\d,]*(?:\.\d+)?)\s*([kmgt]?i?b|bytes?)\b`)

// parsePMTilesDryRun reads the byte total of `pmtiles extract --dry-run` from its output: the first
// size on a line that says "tiles to extract" (any case), else on a "total size" line. Sizes are
// "1234", "4.2 MB" (decimal) or "3.9 MiB" (binary). ok is false when the output has none, and the caller
// falls back to the catalog's expected_size_bytes.
func parsePMTilesDryRun(out string) (int64, bool) {
	for _, key := range []string{"tiles to extract", "total size"} {
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(strings.ToLower(line), key) {
				continue
			}
			if n, ok := parseSizeToken(line); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func parseSizeToken(line string) (int64, bool) {
	m := pmtilesSizeRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	unit := strings.ToLower(m[2])
	mult := 1.0
	switch unit {
	case "b", "byte", "bytes":
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "kib":
		mult = 1 << 10
	case "mib":
		mult = 1 << 20
	case "gib":
		mult = 1 << 30
	case "tib":
		mult = 1 << 40
	default:
		return 0, false
	}
	n := v * mult
	if n > float64(math.MaxInt64/2) {
		return 0, false
	}
	return int64(n), true
}

// dryRun asks the tool how many bytes the extract would fetch. Any failure is "unknown": the caller
// falls back to expected_size_bytes.
func (p *PMTiles) dryRun(ctx context.Context, bin string, e pmtilesExpect) (int64, bool) {
	res, err := subproc.Run(ctx, p.Env.runner(), subproc.Spec{
		Bin: bin, Home: p.Env.SyncHome, Credential: p.Credential, Timeout: pmtilesQuickTimeout, MaxOut: smallOut,
		Args: []string{"extract", e.planet, "/dev/null", "--bbox=" + e.bboxArg(), "--maxzoom=" + strconv.Itoa(e.maxZoom), "--dry-run"},
	})
	if err != nil || !res.OK() {
		return 0, false
	}
	return parsePMTilesDryRun(res.Stdout + "\n" + res.Stderr)
}

// Resolve implements plan.Resolver: the newest build date as the version, and the size the dry run
// estimates (the catalog's expected_size_bytes when the tool does not say). A missing tool is LAI-SYN-061
// and leaves the item unresolved.
func (p *PMTiles) Resolve(ctx context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	s, err := p.source(it)
	if err != nil {
		return plan.Resolved{}, err
	}
	if err := p.Env.requireOnline(); err != nil {
		return plan.Resolved{}, err
	}
	bin, err := p.checkTool()
	if err != nil {
		return plan.Resolved{}, err
	}
	date, err := p.newest(ctx, s)
	if err != nil {
		return plan.Resolved{}, err
	}
	size, ok := p.dryRun(ctx, bin, s.expect(date))
	if !ok {
		size = s.expected
	}
	return plan.Resolved{Version: date, SortKey: date, SizeBytes: size}, nil
}

// ---- the engine's half --------------------------------------------------------------------------------

// ResolveItem is the engine's half (run.ItemResolver, bound with run.ItemResolverFunc). The item is a
// directory item: <id>.pmtiles inside maps/<id>/. It is built into the staged tree by Build, under the
// "pmtiles" exclusive resource and the network slot the machine holds, trust computed.
func (p *PMTiles) ResolveItem(ctx context.Context, it plan.CatalogItem, _ plan.ItemPlan) (item.Resolution, error) {
	res, err := p.resolveItem(ctx, it)
	if err != nil {
		return item.Resolution{}, pmtilesItemErr(err)
	}
	return res, nil
}

func (p *PMTiles) resolveItem(ctx context.Context, it plan.CatalogItem) (item.Resolution, error) {
	s, err := p.source(it)
	if err != nil {
		return item.Resolution{}, err
	}
	if err := p.Env.requireOnline(); err != nil {
		return item.Resolution{}, err
	}
	if _, err := p.checkTool(); err != nil {
		return item.Resolution{}, err
	}
	date, err := p.newest(ctx, s)
	if err != nil {
		return item.Resolution{}, err
	}
	e := s.expect(date)
	src := manifest.Source{Type: "pmtiles_extract"}
	for _, kv := range []struct {
		k string
		v any
	}{{"planet_url", e.planet}, {"build_date", date}, {"bbox", e.bboxArg()}, {"maxzoom", e.maxZoom}} {
		if src, err = src.With(kv.k, kv.v); err != nil {
			return item.Resolution{}, fail(CodeRefused, err, "item %s: a source field is not usable", it.ID)
		}
	}
	p.expect.Store(it.ID, e)
	return item.Resolution{
		Version: date, SortKey: date, Source: src, Trust: manifest.TrustComputed, Resolver: "pmtiles_extract",
		Dir: true, Exclusive: pmtilesExclusive,
		Build: func(ctx context.Context, treeDir string) error {
			if err := p.build(ctx, e, treeDir); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return pmtilesItemErr(err)
			}
			return nil
		},
	}, nil
}

// pmtilesItemErr maps a resolver error to the engine's *item.ResolveError, keeping the doctor code. A
// missing tool, an unusable catalog entry and the offline refusal are permanent for this run; the rest
// is retried by the next run.
func pmtilesItemErr(err error) error {
	var re *item.ResolveError
	if errors.As(err, &re) {
		return err
	}
	code := CodeOf(err)
	if code == "" {
		code = CodeNoVersion
	}
	return &item.ResolveError{Code: code, Permanent: code == CodeTool || code == CodeRefused || code == CodeOffline, Err: err}
}

// ---- the extract --------------------------------------------------------------------------------------

func (p *PMTiles) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// build runs `pmtiles extract` into treeDir/<id>.pmtiles.part and renames it to <id>.pmtiles when the
// tool exits 0 (so a partial file never has the final name). The CLI cannot resume, so a leftover of an
// earlier run is removed and the extract starts from scratch (the journal item is not resumable). Two
// attempts per run, a pause between them. The tool is throttled through the shared bandwidth bucket
// when a limit is set: HTTPS_PROXY points at the loopback proxy, which allows the planet host only.
func (p *PMTiles) build(ctx context.Context, e pmtilesExpect, treeDir string) error {
	if err := p.Env.requireOnline(); err != nil {
		return err
	}
	bin, err := p.checkTool()
	if err != nil {
		return err
	}
	out := filepath.Join(treeDir, e.fileName)
	part := out + ".part"
	for _, f := range []string{part, out} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fail(CodeFetch, err, "cannot clear an earlier extract")
		}
	}
	restore, err := p.lend(treeDir)
	if err != nil {
		return fail(CodeFetch, err, "cannot hand the staging folder to the service user")
	}
	defer restore()

	proxy, stop, perr := p.Env.startProxy(ctx, []string{e.host})
	if perr != nil {
		return fail(CodeFetch, perr, "cannot start the bandwidth limiter")
	}
	defer stop()
	var env []string
	if proxy != "" {
		env = append(env, "HTTPS_PROXY=http://"+proxy)
	}
	timeout := p.ExtractTimeout
	if timeout <= 0 {
		timeout = pmtilesExtractTimeout
	}
	delay := p.RetryDelay
	if delay <= 0 {
		delay = pmtilesRetryDelay
	}

	var last error
	for attempt := 1; attempt <= pmtilesAttempts; attempt++ {
		if attempt > 1 {
			if err := p.sleep(ctx, delay); err != nil {
				return err
			}
			if err := os.Remove(part); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fail(CodeFetch, err, "cannot clear the failed extract")
			}
		}
		res, err := subproc.Run(ctx, p.Env.runner(), subproc.Spec{
			Bin: bin, Home: p.Env.SyncHome, Env: env, Credential: p.Credential, Timeout: timeout, MaxOut: smallOut,
			Args: []string{
				"extract", e.planet, part, "--bbox=" + e.bboxArg(), "--maxzoom=" + strconv.Itoa(e.maxZoom),
				"--download-threads=4", "--overfetch=0.05",
			},
		})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, subproc.ErrTimeout):
			return fail(CodeFetch, err, "the map extract of %s took longer than %s", e.id, timeout)
		case errors.Is(err, subproc.ErrStart), errors.Is(err, subproc.ErrInvalid):
			return fail(CodeTool, err, "pmtiles could not be started")
		case err != nil:
			return fail(CodeFetch, err, "the map extract of %s could not run", e.id)
		case !res.OK():
			last = fmt.Errorf("pmtiles extract exited %d: %s", res.ExitCode, res.Tail(300))
			p.Env.log().Warn("pmtiles extract failed", "item", e.id, "attempt", attempt, "exit", res.ExitCode)
			continue
		}
		st, serr := os.Lstat(part)
		if serr != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			last = errors.New("pmtiles extract exited 0 but wrote no file")
			continue
		}
		if err := os.Rename(part, out); err != nil {
			return fail(CodeFetch, err, "cannot name the finished extract")
		}
		return nil
	}
	return fail(CodeFetch, last, "the map extract of %s failed %d times", e.id, pmtilesAttempts)
}

// lend gives the staged tree to the service user for the duration of the extract and returns the
// function that hands it back. The tool runs as that user and the staging folders are 0700 root, so the
// tree is chowned to it and every folder from the tree up to .staging gets the search bit for others
// (never read): the modes and owners are restored when the tool is done, and the layout check of the
// content root puts .staging back to 0700 at the next start if the agent died in between. Without a
// Credential (an unprivileged development run) nothing changes.
func (p *PMTiles) lend(treeDir string) (restore func(), err error) {
	if p.Credential == nil {
		return func() {}, nil
	}
	stat, chown, chmod := p.Stat, p.Chown, p.Chmod
	if stat == nil {
		stat = os.Stat
	}
	if chown == nil {
		chown = os.Chown
	}
	if chmod == nil {
		chmod = os.Chmod
	}
	euid, egid := os.Geteuid(), os.Getegid()
	if p.Euid != nil {
		euid = p.Euid()
	}
	if p.Egid != nil {
		egid = p.Egid()
	}
	uid, gid := int(p.Credential.UID), int(p.Credential.GID)

	type saved struct {
		dir  string
		mode os.FileMode
	}
	var chain []saved
	found := false
	dir := treeDir
	for i := 0; i < 8 && !found; i++ {
		dir = filepath.Dir(dir)
		st, serr := stat(dir)
		if serr != nil {
			break
		}
		chain = append(chain, saved{dir, st.Mode().Perm()})
		found = filepath.Base(dir) == ".staging"
	}
	if !found {
		chain = nil // not below a .staging folder (a development run): only the tree itself is lent
	}
	treeMode := os.FileMode(0o755)
	if st, serr := stat(treeDir); serr == nil {
		treeMode = st.Mode().Perm()
	}
	done := 0
	restore = func() {
		for i := done - 1; i >= 0; i-- {
			_ = chmod(chain[i].dir, chain[i].mode)
		}
		if ents, rerr := os.ReadDir(treeDir); rerr == nil {
			for _, ent := range ents {
				if !ent.IsDir() {
					_ = chown(filepath.Join(treeDir, ent.Name()), euid, egid)
				}
			}
		}
		_ = chown(treeDir, euid, egid)
		_ = chmod(treeDir, treeMode)
	}
	for _, c := range chain {
		if c.mode&0o001 == 0 {
			if err := chmod(c.dir, c.mode|0o001); err != nil {
				restore()
				return nil, err
			}
		}
		done++
	}
	if err := chown(treeDir, uid, gid); err != nil {
		restore()
		return nil, err
	}
	if err := chmod(treeDir, 0o755); err != nil {
		restore()
		return nil, err
	}
	return restore, nil
}

// ---- the validator ------------------------------------------------------------------------------------

// PMTilesError is an extract that failed its checks. It carries LAI-SYN-023 (item.Validators reads
// DoctorCode), so the item is quarantined and rebuilt on the next run.
type PMTilesError struct{ Msg string }

// Error implements error.
func (e *PMTilesError) Error() string { return CodePMTilesVerify + ": " + e.Msg }

// DoctorCode returns LAI-SYN-023.
func (*PMTilesError) DoctorCode() string { return CodePMTilesVerify }

// PMTilesValidatorName is the name item.Validators shows in reason.json for the pmtiles check.
const PMTilesValidatorName = "pmtiles_verify"

// RegisterValidator registers the pmtiles checks on the item machine's content validators: for every
// staged tree the resolver built, `pmtiles verify` must exit 0, and `pmtiles show --header-json` must
// report tile_type mvt and a bounding box that contains the catalog's to within 0.01 degrees. A tree
// that this process did not build (a copy from another root or a drive, already proven by its source's
// manifest) is not checked again.
func (p *PMTiles) RegisterValidator(v *item.Validators) error {
	return v.Register(PMTilesValidatorName,
		func(m item.Meta) bool { return m.SourceType == "pmtiles_extract" },
		func(ctx context.Context, t item.Target) error {
			x, ok := p.expect.Load(t.ItemID)
			if !ok {
				return nil
			}
			return p.validate(ctx, x.(pmtilesExpect), t.StagedDir)
		})
}

func (p *PMTiles) validate(ctx context.Context, e pmtilesExpect, dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var maps []string
	for _, ent := range ents {
		if ent.Type().IsRegular() && strings.HasSuffix(ent.Name(), ".pmtiles") {
			maps = append(maps, ent.Name())
		}
	}
	sort.Strings(maps)
	if len(maps) != 1 || maps[0] != e.fileName {
		return &PMTilesError{Msg: fmt.Sprintf("the extract holds %d map files, expected only %s", len(maps), e.fileName)}
	}
	file := filepath.Join(dir, maps[0])
	bin, err := p.checkTool()
	if err != nil {
		return err
	}
	res, err := subproc.Run(ctx, p.Env.runner(), subproc.Spec{
		Bin: bin, Args: []string{"verify", file}, Home: p.Env.SyncHome, Credential: p.Credential, Timeout: pmtilesVerifyTimeout, MaxOut: smallOut,
	})
	if err != nil {
		return p.toolErr(ctx, err, "pmtiles verify")
	}
	if !res.OK() {
		return &PMTilesError{Msg: fmt.Sprintf("pmtiles verify exited %d: %s", res.ExitCode, res.Tail(300))}
	}
	res, err = subproc.Run(ctx, p.Env.runner(), subproc.Spec{
		Bin: bin, Args: []string{"show", file, "--header-json"}, Home: p.Env.SyncHome, Credential: p.Credential, Timeout: pmtilesQuickTimeout, MaxOut: smallOut,
	})
	if err != nil {
		return p.toolErr(ctx, err, "pmtiles show")
	}
	if !res.OK() {
		return &PMTilesError{Msg: fmt.Sprintf("pmtiles show exited %d: %s", res.ExitCode, res.Tail(300))}
	}
	return checkPMTilesHeader([]byte(res.Stdout), e.bbox)
}

// toolErr turns a failure to run the tool into an error: the context's own when it ended, else a
// PMTilesError (a tool that cannot run cannot vouch for the file).
func (p *PMTiles) toolErr(ctx context.Context, err error, what string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &PMTilesError{Msg: what + " could not run: " + Clip([]byte(err.Error()), 200)}
}

// pmtilesHeader is the part of `pmtiles show --header-json` the check reads. The tool writes the
// bounds as E7 integers (min_lon_e7 ...); plain degrees (min_lon ...) are accepted too, and the tile
// type as "mvt" or as the number 1 of the PMTiles spec.
type pmtilesHeader struct {
	TileType json.RawMessage `json:"tile_type"`
	MinLonE7 *float64        `json:"min_lon_e7"`
	MinLatE7 *float64        `json:"min_lat_e7"`
	MaxLonE7 *float64        `json:"max_lon_e7"`
	MaxLatE7 *float64        `json:"max_lat_e7"`
	MinLon   *float64        `json:"min_lon"`
	MinLat   *float64        `json:"min_lat"`
	MaxLon   *float64        `json:"max_lon"`
	MaxLat   *float64        `json:"max_lat"`
}

func (h pmtilesHeader) bounds() (b [4]float64, ok bool) {
	pick := func(e7, deg *float64) (float64, bool) {
		switch {
		case e7 != nil:
			return *e7 / 1e7, true
		case deg != nil:
			return *deg, true
		}
		return 0, false
	}
	var oks [4]bool
	b[0], oks[0] = pick(h.MinLonE7, h.MinLon)
	b[1], oks[1] = pick(h.MinLatE7, h.MinLat)
	b[2], oks[2] = pick(h.MaxLonE7, h.MaxLon)
	b[3], oks[3] = pick(h.MaxLatE7, h.MaxLat)
	return b, oks[0] && oks[1] && oks[2] && oks[3]
}

func (h pmtilesHeader) isMVT() bool {
	var s string
	if json.Unmarshal(h.TileType, &s) == nil {
		return strings.EqualFold(strings.TrimSpace(s), "mvt")
	}
	var n int
	if json.Unmarshal(h.TileType, &n) == nil {
		return n == 1
	}
	return false
}

// checkPMTilesHeader checks the header JSON against the catalog bounding box: the tile type is mvt and
// the header's box contains the catalog's, each edge to within 0.01 degrees.
func checkPMTilesHeader(raw []byte, want [4]float64) error {
	var h pmtilesHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return &PMTilesError{Msg: "pmtiles show did not print a JSON header"}
	}
	if !h.isMVT() {
		return &PMTilesError{Msg: "the map is not a vector (mvt) tile set"}
	}
	got, ok := h.bounds()
	if !ok {
		return &PMTilesError{Msg: "the map header has no bounding box"}
	}
	const tol = pmtilesBBoxTolerance
	if got[0] > want[0]+tol || got[1] > want[1]+tol || got[2] < want[2]-tol || got[3] < want[3]-tol {
		return &PMTilesError{Msg: fmt.Sprintf("the map covers [%.4f,%.4f,%.4f,%.4f], less than the requested [%.4f,%.4f,%.4f,%.4f]",
			got[0], got[1], got[2], got[3], want[0], want[1], want[2], want[3])}
	}
	return nil
}
