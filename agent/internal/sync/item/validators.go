package item

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Meta is what a validator's matcher may look at: the identity of the item and where it comes from.
type Meta struct {
	ItemID     string
	Kind       string
	Engine     string
	SourceType string
}

// Target is what a validator may read: only the staged files. StagedDir is the directory the files
// are in (the tree for a directory item), Files their relative paths.
type Target struct {
	Meta
	StagedDir string
	Files     []File
	Dir       bool
}

// ValidateFunc checks the staged content of an item after its hashes matched. An error is a
// verification failure: the item is quarantined with reason.json {"validator", "error"}. An error
// that has a DoctorCode() string method decides the code (LAI-SYN-023 for pmtiles verify, LAI-SYN-028
// for the ZIM checksum); otherwise it is LAI-SYN-020. Validators read the staged files and nothing
// else, and must not modify them.
type ValidateFunc func(ctx context.Context, t Target) error

type validator struct {
	name  string
	match func(Meta) bool
	fn    ValidateFunc
}

// Validators is the registry of content validators (ark-sync.md section 4.5, "Content validators").
// It is safe for concurrent use. The resolvers' issues register theirs (pmtiles verify, the ZIM
// internal checksum, the GeoNames plausibility check): that is how a trust: tofu item gets a content
// check in place of a pinned hash.
type Validators struct {
	mu sync.RWMutex
	vs []validator
}

// NewValidators returns an empty registry.
func NewValidators() *Validators { return &Validators{} }

// Register adds a validator. name is what reason.json shows; match chooses the items it applies to.
func (v *Validators) Register(name string, match func(Meta) bool, fn ValidateFunc) error {
	if name == "" || match == nil || fn == nil {
		return errors.New("item: a validator needs a name, a matcher and a function")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range v.vs {
		if e.name == name {
			return fmt.Errorf("item: validator %q is already registered", name)
		}
	}
	v.vs = append(v.vs, validator{name: name, match: match, fn: fn})
	return nil
}

// ValidationError is a failed validator.
type ValidationError struct {
	Validator string
	Code      string
	Err       error
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: validator %s: %v", e.Code, e.Validator, e.Err)
}

// Unwrap returns the validator's own error.
func (e *ValidationError) Unwrap() error { return e.Err }

// Run calls every validator that matches, in registration order, and returns the first failure as a
// *ValidationError. A context error is returned as it is.
func (v *Validators) Run(ctx context.Context, t Target) error {
	if v == nil {
		return nil
	}
	v.mu.RLock()
	list := append([]validator(nil), v.vs...)
	v.mu.RUnlock()
	for _, e := range list {
		if !e.match(t.Meta) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.fn(ctx, t); err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return err
			}
			code := doctor.SYN020
			var c interface{ DoctorCode() string }
			if errors.As(err, &c) && c.DoctorCode() != "" {
				code = c.DoctorCode()
			}
			return &ValidationError{Validator: e.name, Code: code, Err: err}
		}
	}
	return nil
}
