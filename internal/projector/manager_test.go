package projector

import (
	"context"
	"iter"
	"testing"
	"time"

	vrclog "github.com/vrclog/vrclog-go"

	"github.com/vrclog/vrclog-companion/internal/observation"
)

func seqOf(items ...observation.StoredObservation) iter.Seq2[observation.StoredObservation, error] {
	return func(yield func(observation.StoredObservation, error) bool) {
		for i, it := range items {
			it.Sequence = int64(i + 1)
			if !yield(it, nil) {
				return
			}
		}
	}
}

func TestManager_RebuildSuppressesChangesButBuildsState(t *testing.T) {
	base := time.Now().UTC()
	obs := []observation.StoredObservation{
		joiningObs("j1", "wrld_1", "inst_1", base),
		playerJoinedObs("p1", "Alice", base.Add(time.Second)),
	}

	m := NewManager()
	if err := m.Rebuild(context.Background(), seqOf(obs...)); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	snap := m.Snapshot()
	if snap.World == nil || snap.World.InstanceID != "inst_1" {
		t.Fatalf("Snapshot().World = %+v, want inst_1", snap.World)
	}
	if len(snap.Players) != 1 || snap.Players[0].DisplayName != "Alice" {
		t.Fatalf("Snapshot().Players = %+v, want [Alice]", snap.Players)
	}
}

func TestManager_RebuildIsDeterministicWithLiveApply(t *testing.T) {
	base := time.Now().UTC()
	obs := []observation.StoredObservation{
		joiningObs("j1", "wrld_1", "inst_1", base),
		enteringObs("e1", "Cool World", base.Add(500*time.Millisecond)),
		playerJoinedObs("p1", "Alice", base.Add(time.Second)),
		playerJoinedObs("p2", "Bob", base.Add(2*time.Second)),
		playerLeftObs("p3", "Alice", base.Add(3*time.Second)),
		joiningObs("j2", "wrld_2", "inst_2", base.Add(time.Minute)),
		playerJoinedObs("p4", "Carol", base.Add(61*time.Second)),
	}

	live := NewManager()
	for _, o := range obs {
		if _, err := live.Apply(o); err != nil {
			t.Fatalf("live Apply(%s): %v", o.ID, err)
		}
	}

	rebuilt := NewManager()
	if err := rebuilt.Rebuild(context.Background(), seqOf(obs...)); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	liveSnap, rebuiltSnap := live.Snapshot(), rebuilt.Snapshot()

	if *liveSnap.World != *rebuiltSnap.World {
		t.Fatalf("World mismatch: live=%+v rebuilt=%+v", liveSnap.World, rebuiltSnap.World)
	}
	if len(liveSnap.Players) != len(rebuiltSnap.Players) {
		t.Fatalf("Players count mismatch: live=%d rebuilt=%d", len(liveSnap.Players), len(rebuiltSnap.Players))
	}
	for i := range liveSnap.Players {
		if liveSnap.Players[i] != rebuiltSnap.Players[i] {
			t.Fatalf("Players[%d] mismatch: live=%+v rebuilt=%+v", i, liveSnap.Players[i], rebuiltSnap.Players[i])
		}
	}
}

// TestManager_RebuildTwiceIsIdempotent pins the WARNING fix: Rebuild must
// replay into fresh projector state and swap it in, not replay on top of
// whatever the Manager already holds. Before the fix, a second Rebuild call
// (e.g. after PRAGMA-driven cache invalidation, or a manual re-rebuild)
// would apply the new observation set on top of leftover state from the
// first Rebuild instead of starting clean.
func TestManager_RebuildTwiceIsIdempotent(t *testing.T) {
	base := time.Now().UTC()
	first := []observation.StoredObservation{
		joiningObs("j1", "wrld_1", "inst_1", base),
		playerJoinedObs("p1", "Alice", base.Add(time.Second)),
		playerJoinedObs("p2", "Bob", base.Add(2*time.Second)),
	}

	m := NewManager()
	if err := m.Rebuild(context.Background(), seqOf(first...)); err != nil {
		t.Fatalf("first Rebuild: %v", err)
	}
	firstSnap := m.Snapshot()
	if firstSnap.World == nil || firstSnap.World.InstanceID != "inst_1" {
		t.Fatalf("after first Rebuild, World = %+v, want inst_1", firstSnap.World)
	}
	if len(firstSnap.Players) != 2 {
		t.Fatalf("after first Rebuild, Players = %+v, want 2", firstSnap.Players)
	}

	// Rebuild again with a disjoint, smaller observation set. A correct
	// swap-based Rebuild reflects ONLY this set afterwards; a buggy
	// in-place replay would still show inst_1 and/or Alice/Bob left over
	// from the first call.
	second := []observation.StoredObservation{
		joiningObs("j2", "wrld_2", "inst_2", base.Add(time.Minute)),
	}
	if err := m.Rebuild(context.Background(), seqOf(second...)); err != nil {
		t.Fatalf("second Rebuild: %v", err)
	}
	secondSnap := m.Snapshot()
	if secondSnap.World == nil || secondSnap.World.InstanceID != "inst_2" {
		t.Fatalf("after second Rebuild, World = %+v, want inst_2 (leftover state from first Rebuild)", secondSnap.World)
	}
	if len(secondSnap.Players) != 0 {
		t.Fatalf("after second Rebuild, Players = %+v, want none (leftover state from first Rebuild)", secondSnap.Players)
	}

	// Rebuilding with the exact same input again must reproduce an
	// identical snapshot.
	if err := m.Rebuild(context.Background(), seqOf(second...)); err != nil {
		t.Fatalf("third Rebuild: %v", err)
	}
	thirdSnap := m.Snapshot()
	if *thirdSnap.World != *secondSnap.World {
		t.Fatalf("repeated Rebuild with same input not idempotent: got %+v, want %+v", thirdSnap.World, secondSnap.World)
	}
	if len(thirdSnap.Players) != len(secondSnap.Players) {
		t.Fatalf("repeated Rebuild with same input not idempotent: Players = %+v, want %+v", thirdSnap.Players, secondSnap.Players)
	}
}

// TestMedia_CorrelationWindowBoundary pins the exact-10s inclusive
// boundary per the Round-3 adversarial review: <= must be used
// consistently so live application and startup rebuild never diverge on a
// borderline timestamp delta.
func TestMedia_CorrelationWindowBoundary(t *testing.T) {
	cases := []struct {
		name      string
		delta     time.Duration
		wantMerge bool
	}{
		{"9.999s merges", 9999 * time.Millisecond, true},
		{"10.000s merges (inclusive boundary)", 10000 * time.Millisecond, true},
		{"10.001s does not merge", 10001 * time.Millisecond, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			base := time.Now().UTC()
			applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

			applyOne(t, m, resourceURLObs(t, "o1",
				vrclog.RemoteResource{URL: "https://example.com/a", Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolverInput},
				nil, "vrchat.core", base.Add(1*time.Second)))

			applyOne(t, m, mediaErrorObs(t, "o2", vrclog.MediaStagePlayback, "err",
				nil, nil, "vrchat.core", base.Add(1*time.Second+tc.delta)))

			recent := m.RecentMedia(0)
			gotMerge := len(recent) == 1
			if gotMerge != tc.wantMerge {
				t.Fatalf("delta=%s: got %d attempts (merge=%v), want merge=%v", tc.delta, len(recent), gotMerge, tc.wantMerge)
			}
		})
	}
}
