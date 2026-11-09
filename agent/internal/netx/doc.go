// Package netx is the agent's one door to the network (drill.md section 4.4). Every outbound HTTP
// client and every raw dial of the agent goes through it, so the drill's network fence can see them:
//
//   - Shared is the single shared outbound http.Transport, NewTransport makes a transport with
//     different settings that dials the same way, Client and Via give a client or a round tripper that
//     names its caller, and DialContext / Wrap are the fenced dial for code that dials without HTTP.
//   - While a fence is held (Fence.Acquire, the drill does it for a run) the dial refuses everything but
//     loopback and Unix sockets, before any name is resolved, and counts each refusal with who tried
//     (WithCaller). The fence only restricts: it never opens anything, it is reference counted, and
//     the holder releases it with a deferred Release.
//   - UnixTransport talks to a Unix socket, which the fence never refuses.
//   - DeadProxyEnv is the environment of a subprocess started during the drill.
//
// The guard test of this package fails the build when code outside it creates its own http.Transport,
// uses http.DefaultTransport or dials with net.Dialer, because such a client would reach the internet
// during a drill without being seen. Processes that are not the agent daemon (aictl, the installer) are
// listed in the guard with the reason they are exempt.
package netx
