package netx

import "strings"

// DeadProxy is the proxy address a subprocess gets during the drill: the discard port, where nothing
// listens, so a tool that reads the proxy variables fails at once instead of reaching the internet.
const DeadProxy = "http://127.0.0.1:9"

// DeadProxyEnv returns base (KEY=VALUE entries) for a subprocess started during the drill: every proxy
// variable is removed, in any letter case (http_proxy, HTTPS_PROXY, ALL_PROXY, NO_PROXY, ...), and
// HTTP_PROXY and HTTPS_PROXY are set to DeadProxy. The order of the other entries is kept.
func DeadProxyEnv(base []string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if isProxyVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HTTP_PROXY="+DeadProxy, "HTTPS_PROXY="+DeadProxy)
}

// isProxyVar is any variable that configures a proxy: HTTP_PROXY, https_proxy, ALL_PROXY, NO_PROXY,
// FTP_PROXY, ...
func isProxyVar(name string) bool { return strings.HasSuffix(strings.ToUpper(name), "_PROXY") }
