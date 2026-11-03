// Package health is the health supervisor of the companion services (companion-services.md section
// 4.6, agent.md section 4.3): one state record per service (kiwix, whisper, piper, maps), a liveness
// probe every 30 seconds, a restart budget, the service.state events, the doctor checks and the rows
// of GET /services and GET /status.
//
// llama is not here: llama-router owns it and services.Watcher lists it. This package never talks to
// systemd, the network or a real service directly. systemd sits behind services.Manager, every probe
// behind the Probe interface, time behind config.Clock, so the whole package is tested with fakes and
// a manual clock (internal/services/fake, internal/config/fake). The service items plug their probes
// in with a Spec (SVC-03 kiwix library, SVC-06 whisper env, SVC-08 piper, SVC-09 and SVC-11 maps);
// wiring it all is AGENT-20 and SVC-* issues, never done here.
package health
