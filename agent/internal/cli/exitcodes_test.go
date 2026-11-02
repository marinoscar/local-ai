package cli

import (
	"context"
	"errors"
	"net/http"
	"syscall"
	"testing"
)

func TestExitForStatus(t *testing.T) {
	cases := map[int]int{
		200: ExitOK, 201: ExitOK, 202: ExitOK, 204: ExitOK,
		400: ExitUsage, 413: ExitUsage, 415: ExitUsage,
		401: ExitDenied, 403: ExitDenied,
		404: ExitNotFound,
		409: ExitRefused, 422: ExitRefused, 429: ExitRefused,
		500: ExitFailed, 502: ExitFailed,
		503: ExitUnreachable,
		504: ExitTimeout,
		// outside the table: other 4xx are invalid requests, everything else not 2xx is a failure
		405: ExitUsage, 410: ExitUsage, 418: ExitUsage, 501: ExitFailed, 302: ExitFailed,
	}
	for status, want := range cases {
		if got := ExitForStatus(status); got != want {
			t.Errorf("%d %s -> %d, want %d", status, http.StatusText(status), got, want)
		}
	}
}

func TestExitFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"exit error", ExitWith(ExitWarnings), ExitWarnings},
		{"exit error errors", ExitWith(ExitErrors), ExitErrors},
		{"cli error", NewCLIError(LangEN, CodeNeedRoot, ""), ExitDenied},
		{"wrapped cli error", errors.Join(errors.New("x"), NewCLIError(LangEN, CodeUsage, "")), ExitUsage},
		{"api error", &APIError{Status: 404}, ExitNotFound},
		{"deadline", context.DeadlineExceeded, ExitTimeout},
		{"canceled", context.Canceled, ExitInterrupted},
		{"broken pipe", syscall.EPIPE, ExitOK},
		{"other", errors.New("boom"), ExitFailed},
	}
	for _, tc := range cases {
		if got := ExitFor(tc.err); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
	// Every code aictl raises carries the exit code its table row says.
	for code, want := range map[string]int{
		CodeUnreachable: ExitUnreachable, CodePermission: ExitDenied, CodeVersionSkew: ExitOK, CodeTimeout: ExitTimeout,
		CodeUsage: ExitUsage, CodeNeedRoot: ExitDenied, CodeBadResponse: ExitFailed, CodeInterrupted: ExitInterrupted,
	} {
		if got := ExitFor(NewCLIError(LangEN, code, "")); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
	}
}
