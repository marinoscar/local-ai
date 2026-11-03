package api_test

import "testing"

// TestArkDrivesIsImplemented: GET /ark/drives (SYNC-02) is among the implemented routes of the
// bijection test, so its operation in openapi.yaml carries no x-localai-status: planned.
func TestArkDrivesIsImplemented(t *testing.T) {
	found := false
	for _, rt := range implementedRoutes(t) {
		if rt.Method == "GET" && rt.Pattern == "/ark/drives" {
			found = true
		}
	}
	if !found {
		t.Fatal("GET /ark/drives is not among the implemented routes")
	}
	op, ok := spec(t).Match("GET", "/ark/drives")
	if !ok || op.Planned() {
		t.Fatalf("GET /ark/drives in openapi.yaml: found=%v planned=%v", ok, ok && op.Planned())
	}
}
