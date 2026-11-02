package statusapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router"
)

func TestResultOf(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		got := resultOf(router.Result{Written: true, Reloaded: true})
		if !got.Written || !got.Reloaded || len(got.Warnings) != 0 {
			t.Errorf("result = %+v", got)
		}
	})
	t.Run("findings become warnings with their subject", func(t *testing.T) {
		got := resultOf(router.Result{Findings: []router.PresetFinding{
			{Code: router.CodeFilesMissing, Subject: "model-beta-27b-q4"},
			{Code: router.CodeNotFit},
		}})
		if len(got.Warnings) != 2 || got.Warnings[0].Code != doctor.LLM008 || got.Warnings[1].Code != doctor.CAT005 {
			t.Fatalf("warnings = %+v", got.Warnings)
		}
		title, _, _ := doctor.Text(doctor.LLM008, doctor.LangEN)
		if got.Warnings[0].Message != title+" (model-beta-27b-q4)" || got.Warnings[1].Message == "" {
			t.Errorf("messages = %q / %q", got.Warnings[0].Message, got.Warnings[1].Message)
		}
	})
	t.Run("an apply error that is not a router error is busy", func(t *testing.T) {
		got := resultOf(router.Result{Written: true, ApplyErr: errors.New("router: model operations are busy, the reload waits")})
		if len(got.Warnings) != 1 || got.Warnings[0].Code != api.CodeBusy {
			t.Errorf("warnings = %+v", got.Warnings)
		}
	})
	t.Run("an apply error of the router keeps its code", func(t *testing.T) {
		got := resultOf(router.Result{ApplyErr: router.ErrDown})
		if len(got.Warnings) != 1 || got.Warnings[0].Code != string(router.CodeRouterDown) {
			t.Errorf("warnings = %+v", got.Warnings)
		}
	})
}

type errRegen struct{ err error }

func (e errRegen) Regenerate(context.Context) (RegenResult, error) { return RegenResult{}, e.err }

func TestRegenerate(t *testing.T) {
	if got := regenerate(context.Background(), nil); got != nil {
		t.Errorf("nil regenerator: %v", got)
	}
	if got := regenerate(context.Background(), errRegen{errors.New("boom")}); len(got) != 1 || got[0].Code != api.CodeInternal {
		t.Errorf("error: %v", got)
	}
	if got := regenerate(context.Background(), errRegen{context.DeadlineExceeded}); len(got) != 1 || got[0].Code != api.CodeBusy {
		t.Errorf("deadline: %v", got)
	}
	if got := regenerate(context.Background(), errRegen{nil}); len(got) != 0 {
		t.Errorf("clean: %v", got)
	}
}

func TestRouterRegeneratorInputsError(t *testing.T) {
	want := errors.New("no hardware sample yet")
	r := RouterRegenerator{Inputs: func(context.Context) (router.Inputs, error) { return router.Inputs{}, want }}
	if _, err := r.Regenerate(context.Background()); !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
}

func TestCatalogStore(t *testing.T) {
	s := NewCatalogStore(catalog.Loaded{}, time.Time{})
	if got := s.Get(); got.Catalog == nil || got.Source != "" || !got.FetchedAt.IsZero() {
		t.Errorf("empty store = %+v", got)
	}
	cat := catalog.Empty()
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s.Set(catalog.Loaded{Catalog: cat, Source: catalog.SourceKit}, at)
	if got := s.Get(); got.Catalog != cat || got.Source != catalog.SourceKit || !got.FetchedAt.Equal(at) {
		t.Errorf("store = %+v", got)
	}
}

func TestInstalledAt(t *testing.T) {
	dir := t.TempDir()
	if got := InstalledAt(dir); !got.IsZero() {
		t.Errorf("no CATALOG_VERSION: %v", got)
	}
	if got := InstalledAt(""); !got.IsZero() {
		t.Errorf("empty dir name: %v", got)
	}
	p := filepath.Join(dir, "CATALOG_VERSION")
	if err := os.WriteFile(p, []byte("2026.11.02-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	if got := InstalledAt(dir); !got.Equal(at) {
		t.Errorf("InstalledAt = %v, want %v", got, at)
	}
}
