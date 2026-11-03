package build

import (
	"strings"
)

// The messages of the engine were written for llama.cpp (llama-router.md section 4.10) and the llama
// recipe reports them unchanged. For another recipe the one place that speaks for it is this file:
// what leaves the package (Build, Resolve, PreflightReport, the progress lines) is put in the
// recipe's words and under the recipe's doctor codes. Nothing else in the engine knows a second
// program exists, so a fix to a message is a fix for every recipe.

// voiced reports whether the recipe's output needs rewriting.
func (m *Manager) voiced() bool {
	r := m.opt.Recipe
	return r.Name != LlamaRecipe.Name || r.BuildFailedCode != CodeBuildFailed || r.UnusableCode != CodeBuildUnusable
}

// say puts a message in the recipe's words: its program name, its node.yaml block and its command.
// It is idempotent (the replaced words do not appear in the result).
func (m *Manager) say(s string) string {
	if !m.voiced() || s == "" {
		return s
	}
	r := m.opt.Recipe
	return strings.NewReplacer(
		"llama.commit", r.Service+".commit",
		"llama.backend", r.Service+".backend",
		"rebuild-llama", "rebuild-"+r.Service,
		"llama.cpp", r.Name,
	).Replace(s)
}

// code maps a code of the llama recipe to the recipe's own.
func (m *Manager) code(c string) string {
	r := m.opt.Recipe
	switch {
	case c == CodeBuildFailed && r.BuildFailedCode != "":
		return r.BuildFailedCode
	case c == CodeBuildUnusable && r.UnusableCode != "":
		return r.UnusableCode
	}
	return c
}

// voiceErr rewrites the *Error of a build (anything else, a cancelled context included, is returned
// as it is).
func (m *Manager) voiceErr(err error) error {
	e, ok := err.(*Error)
	if !ok || e == nil || !m.voiced() {
		return err
	}
	e.Code = m.code(e.Code)
	e.Message = m.say(e.Message)
	return e
}

// voiceResult rewrites the warnings of a result (its report is voiced where it is made).
func (m *Manager) voiceResult(res Result) Result {
	if !m.voiced() {
		return res
	}
	for i, w := range res.Warnings {
		res.Warnings[i] = m.say(w)
	}
	return res
}

// voiceReport rewrites the checks of a preflight report.
func (m *Manager) voiceReport(rep Report) Report {
	if !m.voiced() {
		return rep
	}
	for i, c := range rep.Checks {
		c.Code = m.code(c.Code)
		c.Title = m.say(c.Title)
		c.Detail = m.say(c.Detail)
		rep.Checks[i] = c
	}
	return rep
}
