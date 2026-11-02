package leakcheck

import "testing"

const pin = "4821"

func TestContains(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want bool
	}{
		// A real leak, wherever it sits, is found.
		{"in the message", `{"time":"2026-11-02T10:00:00.123456789Z","msg":"verify with 4821"}`, true},
		{"in a nested body", `{"time":"2026-11-02T10:00:00Z","body":{"pin":"4821"}}`, true},
		{"in an array", `{"msg":"m","args":["x",["4821"]]}`, true},
		{"as a JSON number in data", `{"msg":"m","code":4821}`, true},
		{"as a key", `{"msg":"m","4821":true}`, true},
		{"in a line that is not JSON", `Admin PIN: 4821`, true},
		{"on the second line", "{\"msg\":\"ok\"}\n{\"msg\":\"pin=4821\"}\n", true},
		{"next to a clean time", `{"note":"4821","time":"2026-11-02T10:00:00Z"}`, true},

		// Digits of the secret inside generated fields are not a leak.
		{"in the nanoseconds of time", `{"time":"2026-11-02T21:09:24.748211482Z","msg":"request"}`, false},
		{"in the nanoseconds of a time under any key", `{"msg":"m","stamp":"2026-11-02T21:24:48.214821Z"}`, false},
		{"in a duration", `{"msg":"response","duration_ms":4821}`, false},
		{"in a port and a pid", `{"msg":"m","port":34821,"pid":4821}`, false},
		{"in a request id", `{"msg":"m","request_id":"req_4821ABCDEFGH"}`, false},
		{"in the status and size", `{"msg":"m","http_status":4821,"size":148211}`, false},
		{"clean log", `{"time":"2026-11-02T21:09:24.1Z","msg":"request","path":"/api/v1/pin/verify"}`, false},
		{"empty", ``, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Contains(tc.log, pin); got != tc.want {
				t.Errorf("Contains = %v, want %v for %s", got, tc.want, tc.log)
			}
		})
	}
	if Contains(`{"msg":"anything"}`, "") {
		t.Error("an empty secret is never found")
	}
}
