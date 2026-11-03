package drill

// Cloud link pause and resume around a run (drill.md section 4.4). The fence refuses every
// non-loopback dial while a drill runs, and the cloud link is the one component that would try to
// reach the internet, so it stands still for the drill's duration: no heartbeat, no state push, no
// claim. The cloud sees a gap in heartbeats, which is normal, and shows "running drill" from the last
// heartbeat before it.

// LinkPauser is the part of the cloud link the drill needs. Pause stops the link's outbound traffic
// without ending its session, and Resume lets it go on and push the state again. Both are safe to call
// when the link is not running. The daemon wiring binds the cloud link here; nothing in internal/cloud
// implements it yet.
type LinkPauser interface {
	Pause()
	Resume()
}

// PausingFence is a Fence that also pauses the cloud link: Enable pauses it and then turns the inner
// fence on; Disable turns the inner fence off, resumes the link (also when the inner fence panics)
// and returns what the inner fence saw. Inner may be nil, which is a fence that refuses nothing, so
// the link still stands still; Link may be nil, which makes it the inner fence alone.
type PausingFence struct {
	Inner Fence
	Link  LinkPauser
}

var _ Fence = (*PausingFence)(nil)

// Enable implements Fence.
func (f *PausingFence) Enable() {
	if f.Link != nil {
		f.Link.Pause()
	}
	if f.Inner != nil {
		f.Inner.Enable()
	}
}

// Disable implements Fence.
func (f *PausingFence) Disable() FenceReport {
	if f.Link != nil {
		defer f.Link.Resume()
	}
	if f.Inner != nil {
		return f.Inner.Disable()
	}
	return FenceReport{}
}
