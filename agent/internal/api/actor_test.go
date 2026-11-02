package api

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/audit"
)

func TestAuditActor(t *testing.T) {
	cases := []struct {
		name string
		info RequestInfo
		kind audit.ActorKind
		via  string
		uid  *int
	}{
		{"anonymous", RequestInfo{}, audit.KindAnonymous, "", nil},
		{"operator bearer", RequestInfo{Principal: Principal{Kind: KindOperator, Credential: CredBearer}}, audit.KindOperator, audit.ViaBearer, nil},
		{"operator session", RequestInfo{Principal: Principal{Kind: KindOperator, Credential: CredSession}}, audit.KindOperator, audit.ViaSession, nil},
		{"admin by PIN", RequestInfo{Principal: Principal{Kind: KindAdmin, Credential: CredSession, Via: "pin"}}, audit.KindAdmin, audit.ViaPIN, nil},
		{"admin on the socket", RequestInfo{Principal: Principal{Kind: KindAdmin, Credential: CredSocket}, Peer: &PeerCred{UID: 0}}, audit.KindAdmin, audit.ViaSocket, intp(0)},
		{"portal", RequestInfo{Principal: Principal{Kind: KindPortal, Credential: CredPortal}}, audit.KindPortal, audit.ViaPortal, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AuditActor(&c.info)
			if got.Kind != c.kind || got.Via != c.via {
				t.Errorf("actor = %+v, want kind %s via %q", got, c.kind, c.via)
			}
			if (got.UID == nil) != (c.uid == nil) || (c.uid != nil && *got.UID != *c.uid) {
				t.Errorf("uid = %v, want %v", got.UID, c.uid)
			}
		})
	}
}

func intp(v int) *int { return &v }
