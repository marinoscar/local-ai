package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Every type has a decoder, and the set is exactly the 14 job names of the config.
func TestHandlersCoverTheFourteenTypes(t *testing.T) {
	f := newFakes()
	var got []string
	for _, h := range Handlers(f.deps()) {
		got = append(got, h.Type())
	}
	want := config.CloudJobNames()
	if !reflect.DeepEqual(got, sortedLike(want, got)) || len(got) != 14 {
		t.Errorf("handlers = %v, want the %d names %v", got, len(want), want)
	}
	if _, err := cloud.NewRegistry(Handlers(f.deps())...); err != nil {
		t.Errorf("the registry refuses the handlers: %v", err)
	}
}

// sortedLike returns names in the order of order (only to compare sets while ignoring the order of
// the handlers, which is this package's choice).
func sortedLike(names, order []string) []string {
	out := make([]string, 0, len(names))
	for _, o := range order {
		for _, n := range names {
			if n == o {
				out = append(out, n)
			}
		}
	}
	return out
}

// A type whose dependency is missing is left out, so the loop refuses it as it would a type missing
// from cloud.allow; agent.update is always there.
func TestHandlersLeaveOutTypesWithoutTheirDependency(t *testing.T) {
	names := func(d Deps) string {
		var out []string
		for _, h := range Handlers(d) {
			out = append(out, h.Type())
		}
		return strings.Join(out, " ")
	}
	if got := names(Deps{}); got != "agent.update" {
		t.Errorf("no dependencies: %s", got)
	}
	f := newFakes()
	d := f.deps()
	d.Ark = nil
	d.Catalog = nil
	got := names(d)
	for _, gone := range []string{"ark.sync", "ark.verify", "catalog.refresh"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s was registered without its dependency", gone)
		}
	}
	if !strings.Contains(got, "model.load") || !strings.Contains(got, "agent.update") {
		t.Errorf("handlers = %s", got)
	}
}

var (
	modelA   = "qwen3.6-35b-a3b-q4km"
	drive    = "ark-3f9c0a12"
	longName = strings.Repeat("a", 65)
)

// decodeCase is one params object and what a decoder must make of it: a problem (path and words) or
// the typed value.
type decodeCase struct {
	name, params string
	path, why    string
	want         any
}

func runDecodeCases(t *testing.T, typ string, cases []decodeCase) {
	t.Helper()
	h := newFakes().handler(t, typ)
	for _, tc := range cases {
		t.Run(typ+"/"+tc.name, func(t *testing.T) {
			got, err := h.Decode(json.RawMessage(tc.params))
			if tc.why != "" {
				var de *cloud.DecodeError
				if !errors.As(err, &de) || de.Path != tc.path || !strings.Contains(de.Problem, tc.why) {
					t.Fatalf("Decode(%s) error = %#v, want path %q problem containing %q", tc.params, err, tc.path, tc.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode(%s): %v", tc.params, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode(%s) = %#v, want %#v", tc.params, got, tc.want)
			}
		})
	}
}

func TestDecodeNoParamTypes(t *testing.T) {
	for _, typ := range []string{"status.refresh", "key.rotate", "catalog.refresh"} {
		runDecodeCases(t, typ, []decodeCase{
			{name: "empty", params: `{}`, want: noParams{}},
			{name: "null", params: `null`, want: noParams{}},
			{name: "an unknown field", params: `{"force":true}`, path: "force", why: "unknown field"},
			{name: "an array", params: `[]`, why: "must be an object"},
		})
	}
}

func TestDecodeModelLoad(t *testing.T) {
	runDecodeCases(t, "model.load", []decodeCase{
		{name: "defaults", params: fmt.Sprintf(`{"model_id":%q}`, modelA), want: ModelLoadParams{ModelID: modelA, UnloadOthers: true}},
		{name: "everything", params: fmt.Sprintf(`{"model_id":%q,"wait":true,"unload_others":false,"set_default":true}`, modelA),
			want: ModelLoadParams{ModelID: modelA, SetDefault: true}},
		{name: "wait false", params: fmt.Sprintf(`{"model_id":%q,"wait":false}`, modelA), path: "wait", why: "must be true"},
		{name: "missing id", params: `{}`, path: "model_id", why: "is required"},
		{name: "id wrong type", params: `{"model_id":7}`, path: "model_id", why: "must be a string"},
		{name: "id with upper case", params: `{"model_id":"Qwen3"}`, path: "model_id", why: "model id"},
		{name: "id too short", params: `{"model_id":"ab"}`, path: "model_id", why: "model id"},
		{name: "id too long", params: fmt.Sprintf(`{"model_id":%q}`, longName), path: "model_id", why: "model id"},
		{name: "id with a slash", params: `{"model_id":"../etc/passwd"}`, path: "model_id", why: "model id"},
		{name: "id starts with a dash", params: `{"model_id":"-abc"}`, path: "model_id", why: "model id"},
		{name: "flag wrong type", params: fmt.Sprintf(`{"model_id":%q,"set_default":"yes"}`, modelA), path: "set_default", why: "true or false"},
		{name: "unknown field", params: fmt.Sprintf(`{"model_id":%q,"args":["--flag"]}`, modelA), path: "args", why: "unknown field"},
	})
}

func TestDecodeModelIDTypes(t *testing.T) {
	for _, typ := range []string{"model.unload", "model.set_default"} {
		runDecodeCases(t, typ, []decodeCase{
			{name: "valid", params: fmt.Sprintf(`{"model_id":%q}`, modelA), want: ModelIDParams{ModelID: modelA}},
			{name: "missing", params: `{}`, path: "model_id", why: "is required"},
			{name: "null", params: `{"model_id":null}`, path: "model_id", why: "must not be null"},
			{name: "extra", params: fmt.Sprintf(`{"model_id":%q,"force":true}`, modelA), path: "force", why: "unknown field"},
		})
	}
}

func TestDecodeModelDownload(t *testing.T) {
	runDecodeCases(t, "model.download", []decodeCase{
		{name: "no roots", params: fmt.Sprintf(`{"model_id":%q}`, modelA), want: ModelDownloadParams{ModelID: modelA}},
		{name: "roots", params: fmt.Sprintf(`{"model_id":%q,"roots":["internal",%q]}`, modelA, drive),
			want: ModelDownloadParams{ModelID: modelA, Roots: []string{"internal", drive}}},
		{name: "empty roots", params: fmt.Sprintf(`{"model_id":%q,"roots":[]}`, modelA), path: "roots", why: "at least 1"},
		{name: "ten roots", params: fmt.Sprintf(`{"model_id":%q,"roots":[%s]}`, modelA, strings.TrimSuffix(strings.Repeat(`"internal",`, 10), ",")), path: "roots", why: "at most 9"},
		{name: "a path as a root", params: fmt.Sprintf(`{"model_id":%q,"roots":["/srv/localai"]}`, modelA), path: "roots[0]", why: "root id"},
		{name: "a short drive id", params: fmt.Sprintf(`{"model_id":%q,"roots":["ark-3f9c"]}`, modelA), path: "roots[0]", why: "root id"},
		{name: "an upper case drive id", params: fmt.Sprintf(`{"model_id":%q,"roots":["ark-3F9C0A12"]}`, modelA), path: "roots[0]", why: "root id"},
		{name: "a root listed twice", params: fmt.Sprintf(`{"model_id":%q,"roots":["internal","internal"]}`, modelA), path: "roots[1]", why: "twice"},
		{name: "roots not an array", params: fmt.Sprintf(`{"model_id":%q,"roots":"internal"}`, modelA), path: "roots", why: "an array"},
		{name: "a number among the roots", params: fmt.Sprintf(`{"model_id":%q,"roots":["internal",3]}`, modelA), path: "roots", why: "a string"},
	})
}

func TestDecodeModelDelete(t *testing.T) {
	ok := fmt.Sprintf(`{"model_id":%q,"roots":[%q],"confirm":%q}`, modelA, drive, modelA)
	runDecodeCases(t, "model.delete", []decodeCase{
		{name: "valid", params: ok, want: ModelDeleteParams{ModelID: modelA, Roots: []string{drive}}},
		{name: "wrong confirmation", params: fmt.Sprintf(`{"model_id":%q,"roots":[%q],"confirm":"other-model"}`, modelA, drive), path: "confirm", why: "must equal model_id"},
		{name: "confirmation in another case", params: fmt.Sprintf(`{"model_id":%q,"roots":[%q],"confirm":%q}`, modelA, drive, strings.ToUpper(modelA)), path: "confirm", why: "must equal model_id"},
		{name: "empty confirmation", params: fmt.Sprintf(`{"model_id":%q,"roots":[%q],"confirm":""}`, modelA, drive), path: "confirm", why: "must equal model_id"},
		{name: "no confirmation", params: fmt.Sprintf(`{"model_id":%q,"roots":[%q]}`, modelA, drive), path: "confirm", why: "is required"},
		{name: "confirmation not a string", params: fmt.Sprintf(`{"model_id":%q,"roots":[%q],"confirm":true}`, modelA, drive), path: "confirm", why: "a string"},
		{name: "no roots", params: fmt.Sprintf(`{"model_id":%q,"confirm":%q}`, modelA, modelA), path: "roots", why: "is required"},
		{name: "empty roots", params: fmt.Sprintf(`{"model_id":%q,"roots":[],"confirm":%q}`, modelA, modelA), path: "roots", why: "at least 1"},
		{name: "a keep-last override is not a field", params: strings.TrimSuffix(ok, "}") + `,"keep_last":false}`, path: "keep_last", why: "unknown field"},
		{name: "a force flag is not a field", params: strings.TrimSuffix(ok, "}") + `,"force":true}`, path: "force", why: "unknown field"},
	})
}

func TestDecodeArkSyncAndVerify(t *testing.T) {
	runDecodeCases(t, "ark.sync", []decodeCase{
		{name: "empty", params: `{}`, want: ArkSyncParams{}},
		{name: "everything", params: `{"roots":["internal"],"items":["wikipedia-en-maxi"],"dry_run":true}`,
			want: ArkSyncParams{Roots: []string{"internal"}, Items: []string{"wikipedia-en-maxi"}, DryRun: true}},
		{name: "dry run wrong type", params: `{"dry_run":1}`, path: "dry_run", why: "true or false"},
		{name: "empty items", params: `{"items":[]}`, path: "items", why: "at least 1"},
		{name: "201 items", params: `{"items":[` + strings.TrimSuffix(strings.Repeat(`"item-abc",`, 201), ",") + `]}`, path: "items", why: "at most 200"},
		{name: "an item with a path", params: `{"items":["../../etc"]}`, path: "items[0]", why: "content id"},
		{name: "a bad root", params: `{"roots":["usb"]}`, path: "roots[0]", why: "root id"},
		{name: "unknown field", params: `{"from":"x"}`, path: "from", why: "unknown field"},
	})
	runDecodeCases(t, "ark.verify", []decodeCase{
		{name: "empty", params: `{}`, want: ArkVerifyParams{}},
		{name: "both", params: `{"roots":["internal"],"items":["wikipedia-en-maxi"]}`, want: ArkVerifyParams{Roots: []string{"internal"}, Items: []string{"wikipedia-en-maxi"}}},
		{name: "a full flag is the CLI's", params: `{"full":true}`, path: "full", why: "unknown field"},
		{name: "a bad item", params: `{"items":["Bad Item"]}`, path: "items[0]", why: "content id"},
	})
}

func TestDecodeModeOffline(t *testing.T) {
	runDecodeCases(t, "mode.offline", []decodeCase{
		{name: "empty", params: `{}`, want: ModeOfflineParams{}},
		{name: "reason", params: `{"reason":"going to the field"}`, want: ModeOfflineParams{Reason: "going to the field"}},
		{name: "200 characters", params: fmt.Sprintf(`{"reason":%q}`, strings.Repeat("r", 200)), want: ModeOfflineParams{Reason: strings.Repeat("r", 200)}},
		{name: "201 characters", params: fmt.Sprintf(`{"reason":%q}`, strings.Repeat("r", 201)), path: "reason", why: "at most 200"},
		{name: "reason wrong type", params: `{"reason":5}`, path: "reason", why: "a string"},
		{name: "a target is not a field", params: `{"target":"survival"}`, path: "target", why: "unknown field"},
	})
}

func TestDecodeServiceRestart(t *testing.T) {
	runDecodeCases(t, "service.restart", []decodeCase{
		{name: "llama", params: `{"service":"llama"}`, want: ServiceRestartParams{Service: "llama"}},
		{name: "kiwix", params: `{"service":"kiwix"}`, want: ServiceRestartParams{Service: "kiwix"}},
		{name: "whisper", params: `{"service":"whisper"}`, want: ServiceRestartParams{Service: "whisper"}},
		{name: "a unit file name", params: `{"service":"localai-llama.service"}`, path: "service", why: "one of"},
		{name: "another service", params: `{"service":"sshd"}`, path: "service", why: "one of"},
		{name: "upper case", params: `{"service":"Llama"}`, path: "service", why: "one of"},
		{name: "missing", params: `{}`, path: "service", why: "is required"},
		{name: "an action is not a field", params: `{"service":"llama","action":"stop"}`, path: "action", why: "unknown field"},
	})
}

func TestDecodeLogsTail(t *testing.T) {
	runDecodeCases(t, "logs.tail", []decodeCase{
		{name: "defaults", params: `{"unit":"agent"}`, want: LogsTailParams{Unit: "agent", Lines: 200}},
		{name: "lines", params: `{"unit":"drill","lines":500}`, want: LogsTailParams{Unit: "drill", Lines: 500}},
		{name: "one line", params: `{"unit":"sync","lines":1}`, want: LogsTailParams{Unit: "sync", Lines: 1}},
		{name: "zero lines", params: `{"unit":"agent","lines":0}`, path: "lines", why: "between 1 and 500"},
		{name: "501 lines", params: `{"unit":"agent","lines":501}`, path: "lines", why: "between 1 and 500"},
		{name: "negative", params: `{"unit":"agent","lines":-1}`, path: "lines", why: "between 1 and 500"},
		{name: "lines as a string", params: `{"unit":"agent","lines":"10"}`, path: "lines", why: "an integer"},
		{name: "lines as a float", params: `{"unit":"agent","lines":10.5}`, path: "lines", why: "an integer"},
		{name: "unit from the closed set only", params: `{"unit":"sshd"}`, path: "unit", why: "one of"},
		{name: "a unit file name", params: `{"unit":"localai-agent.service"}`, path: "unit", why: "one of"},
		{name: "a path", params: `{"unit":"../../var/log/auth.log"}`, path: "unit", why: "one of"},
		{name: "missing unit", params: `{"lines":5}`, path: "unit", why: "is required"},
		{name: "a since filter is not a field", params: `{"unit":"agent","since":"2026-01-01T00:00:00Z"}`, path: "since", why: "unknown field"},
	})
}

func TestDecodeAgentUpdate(t *testing.T) {
	runDecodeCases(t, "agent.update", []decodeCase{
		{name: "a version", params: `{"version":"0.2.0"}`, want: AgentUpdateParams{Version: "0.2.0"}},
		{name: "a prerelease", params: `{"version":"1.2.3-rc.1"}`, want: AgentUpdateParams{Version: "1.2.3-rc.1"}},
		{name: "a v prefix", params: `{"version":"v0.2.0"}`, path: "version", why: "version such as"},
		{name: "two parts", params: `{"version":"0.2"}`, path: "version", why: "version such as"},
		{name: "a url", params: `{"version":"https://example.test/aiagent"}`, path: "version", why: "version such as"},
		{name: "a shell word", params: `{"version":"1.2.3; reboot"}`, path: "version", why: "version such as"},
		{name: "too long", params: fmt.Sprintf(`{"version":"1.2.3-%s"}`, strings.Repeat("a", 64)), path: "version", why: "version such as"},
		{name: "missing", params: `{}`, path: "version", why: "is required"},
		{name: "a url field is not a field", params: `{"version":"0.2.0","url":"https://example.test"}`, path: "url", why: "unknown field"},
	})
}
