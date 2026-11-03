// Package cloud is the agent's cloud link client (agent.md section 4.8, AGENT-17): the node registers
// with the cloud app as an EnterpriseAppBase worker node, heartbeats with vitals, pushes a strict
// state snapshot, reports the llama key and fetches the approved catalog. It is outbound only
// (ADR-0003) and one of the few components allowed to use the internet; everything it does degrades
// into a backoff and never touches a local feature.
//
// What the package owns:
//
//   - Link: the state machine of section 4.8.1 (unenrolled, registering, linked, backoff, disabled,
//     revoked, stopped) and the loops behind it (register, heartbeat, state push, key retry). *Link
//     is the mode engine's modes.LinkController, the statusapi.CloudView, the catalog.CloudSource,
//     the key pusher the key routes call and the implementation of the /cloud routes (api.CloudLink).
//   - Client: the HTTP client for the cloud (one transport, TLS verification always on, https only),
//     the {data, meta} envelope and the error shape of EAB.
//   - The snapshot builder (state.go): LocalaiStateSnapshotV1, built from statusapi's builder and a
//     few seams for what status does not carry (READY.json, drill results, sync runs).
//
// What it does not own: the command plane (claim, renew, settle, inflight.json; AGENT-18). Link
// exposes the seams that loop needs (Session, ClaimOK, ClaimDisabled, ClaimFailed, Counters) and
// starts and stops it through the ClaimLoop interface.
//
// Rules the package keeps: no lock is held across an HTTP call to the cloud (agent.md section 4.9);
// every outgoing string goes through logx.ScrubString and a hit drops the field (LAI-CLD-058); the
// node credential is held in memory and in /etc/localai/cloud-credential only, and is logged as
// nod_<last four>; the llama key is read for the push, sent over https and never logged, only its
// fingerprint is.
package cloud
