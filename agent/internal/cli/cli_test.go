package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMain_(t *testing.T) {
	cases := []struct {
		args       []string
		code       int
		out, errIn string
	}{
		{[]string{"--help"}, ExitOK, "Usage:", ""},
		{[]string{"-h"}, ExitOK, "Usage:", ""},
		{[]string{"help"}, ExitOK, "Usage:", ""},
		{nil, ExitUsage, "", "Usage:"},
		{[]string{"status"}, ExitUsage, "", `unknown command "status"`},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if code := Main(c.args, &out, &errb); code != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.code)
		}
		if !strings.Contains(out.String(), c.out) || !strings.Contains(errb.String(), c.errIn) {
			t.Errorf("%v: stdout %q stderr %q", c.args, out.String(), errb.String())
		}
	}
}
