package projector

import (
	"testing"
	"time"

	vrclog "github.com/vrclog/vrclog-go"

	"github.com/vrclog/vrclog-companion/internal/observation"
)

func resourceURLObs(t *testing.T, id string, resource vrclog.RemoteResource, target *vrclog.MediaTarget, adapterID vrclog.AdapterID, at time.Time) observation.StoredObservation {
	t.Helper()
	obs, err := observation.FromVrclogObservation(vrclog.Observation{
		ID: vrclog.ObservationID(id), Time: at, AdapterID: adapterID,
		Event: vrclog.ResourceURLObserved{Resource: resource, Target: target},
	}, at)
	if err != nil {
		t.Fatalf("FromVrclogObservation: %v", err)
	}
	obs.OccurredAt = at
	return obs
}

func resourceResolvedObs(t *testing.T, id string, input, output vrclog.RemoteResource, target *vrclog.MediaTarget, adapterID vrclog.AdapterID, at time.Time) observation.StoredObservation {
	t.Helper()
	obs, err := observation.FromVrclogObservation(vrclog.Observation{
		ID: vrclog.ObservationID(id), Time: at, AdapterID: adapterID,
		Event: vrclog.ResourceResolved{Input: input, Output: output, Target: target},
	}, at)
	if err != nil {
		t.Fatalf("FromVrclogObservation: %v", err)
	}
	obs.OccurredAt = at
	return obs
}

func mediaErrorObs(t *testing.T, id string, stage vrclog.MediaStage, message string, resource *vrclog.RemoteResource, target *vrclog.MediaTarget, adapterID vrclog.AdapterID, at time.Time) observation.StoredObservation {
	t.Helper()
	obs, err := observation.FromVrclogObservation(vrclog.Observation{
		ID: vrclog.ObservationID(id), Time: at, AdapterID: adapterID,
		Event: vrclog.MediaErrorObserved{Stage: stage, Message: message, Resource: resource, Target: target},
	}, at)
	if err != nil {
		t.Fatalf("FromVrclogObservation: %v", err)
	}
	obs.OccurredAt = at
	return obs
}

func TestMedia_YamaPlayerScenario(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()
	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

	const youtubeURL = "https://www.youtube.com/watch?v=abc123"
	const relayURL = "https://relay.internal/proxy?u=abc123"

	applyOne(t, m, resourceURLObs(t, "o1",
		vrclog.RemoteResource{URL: youtubeURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		nil, "community.yamaplayer", base.Add(1*time.Second)))

	applyOne(t, m, resourceURLObs(t, "o2",
		vrclog.RemoteResource{URL: relayURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolverInput},
		nil, "vrchat.core", base.Add(3*time.Second)))

	applyOne(t, m, mediaErrorObs(t, "o3", vrclog.MediaStagePlayback, "AVPro open failed",
		nil, nil, "vrchat.core", base.Add(5*time.Second)))

	applyOne(t, m, mediaErrorObs(t, "o4", vrclog.MediaStagePlayback, "video error",
		nil, nil, "community.yamaplayer", base.Add(6*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 1 {
		t.Fatalf("RecentMedia = %d attempts, want 1: %+v", len(recent), recent)
	}
	attempt := recent[0]

	if attempt.BestOpenableURL != youtubeURL {
		t.Fatalf("BestOpenableURL = %q, want original YouTube URL %q", attempt.BestOpenableURL, youtubeURL)
	}
	if attempt.Status != MediaStatusFailed {
		t.Fatalf("Status = %q, want failed", attempt.Status)
	}
	foundRelay := false
	for _, r := range attempt.Resources {
		if r.URL == relayURL {
			foundRelay = true
		}
	}
	if !foundRelay {
		t.Fatalf("relay URL missing from Resources: %+v", attempt.Resources)
	}
	hasCore, hasYama := false, false
	for _, a := range attempt.AdapterIDs {
		if a == "vrchat.core" {
			hasCore = true
		}
		if a == "community.yamaplayer" {
			hasYama = true
		}
	}
	if !hasCore || !hasYama {
		t.Fatalf("AdapterIDs = %v, want both vrchat.core and community.yamaplayer", attempt.AdapterIDs)
	}
}

func TestMedia_IwaSync3Scenario(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()
	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

	const sourceURL = "https://example.com/stream.mp4"

	applyOne(t, m, resourceURLObs(t, "o1",
		vrclog.RemoteResource{URL: sourceURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		nil, "vrchat.core", base.Add(1*time.Second)))

	applyOne(t, m, mediaErrorObs(t, "o2", vrclog.MediaStagePlayback, "PlayerError",
		nil, nil, "community.iwasync3", base.Add(3*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 1 {
		t.Fatalf("RecentMedia = %d attempts, want 1: %+v", len(recent), recent)
	}
	attempt := recent[0]
	if attempt.BestOpenableURL != sourceURL {
		t.Fatalf("BestOpenableURL = %q, want %q", attempt.BestOpenableURL, sourceURL)
	}
	if attempt.Status != MediaStatusFailed {
		t.Fatalf("Status = %q, want failed", attempt.Status)
	}
}

func TestMedia_DifferentTargetKeysProduceSeparateAttempts(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()
	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

	target1 := &vrclog.MediaTarget{Component: "AVPro", Key: "player1", Backend: vrclog.MediaBackendAVPro}
	target2 := &vrclog.MediaTarget{Component: "AVPro", Key: "player2", Backend: vrclog.MediaBackendAVPro}

	applyOne(t, m, resourceURLObs(t, "o1",
		vrclog.RemoteResource{URL: "https://a.example.com/1", Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		target1, "vrchat.core", base.Add(1*time.Second)))

	applyOne(t, m, resourceURLObs(t, "o2",
		vrclog.RemoteResource{URL: "https://b.example.com/2", Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		target2, "vrchat.core", base.Add(2*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 2 {
		t.Fatalf("RecentMedia = %d attempts, want 2 (different target keys must not merge): %+v", len(recent), recent)
	}
}

func TestMedia_ResolvedURLNeverBecomesBest(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()
	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

	const inputURL = "https://relay.internal/attempt"
	const signedOutputURL = "https://cdn.example.com/signed?token=abc123"

	applyOne(t, m, resourceURLObs(t, "o1",
		vrclog.RemoteResource{URL: inputURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolverInput},
		nil, "vrchat.core", base.Add(1*time.Second)))

	applyOne(t, m, resourceResolvedObs(t, "o2",
		vrclog.RemoteResource{URL: inputURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolverInput},
		vrclog.RemoteResource{URL: signedOutputURL, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolved},
		nil, "vrchat.core", base.Add(2*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 1 {
		t.Fatalf("RecentMedia = %d attempts, want 1: %+v", len(recent), recent)
	}
	attempt := recent[0]
	if attempt.BestOpenableURL == signedOutputURL {
		t.Fatalf("BestOpenableURL must never be the resolved/signed URL, got %q", attempt.BestOpenableURL)
	}
	if attempt.BestOpenableURL != inputURL {
		t.Fatalf("BestOpenableURL = %q, want resolver_input URL %q", attempt.BestOpenableURL, inputURL)
	}

	foundResolved := false
	for _, r := range attempt.Resources {
		if r.URL == signedOutputURL {
			foundResolved = true
		}
	}
	if !foundResolved {
		t.Fatalf("resolved URL missing from details: %+v", attempt.Resources)
	}
}

func TestMedia_ResolvedOnlyAttemptHasEmptyBest(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()
	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))

	target := &vrclog.MediaTarget{Component: "AVPro", Key: "solo", Backend: vrclog.MediaBackendAVPro}
	applyOne(t, m, resourceResolvedObs(t, "o1",
		vrclog.RemoteResource{URL: "https://relay.internal/x", Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolverInput},
		vrclog.RemoteResource{URL: "https://cdn.example.com/signed?token=xyz", Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleResolved},
		target, "vrchat.core", base.Add(1*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 1 {
		t.Fatalf("RecentMedia = %d attempts, want 1", len(recent))
	}
	if recent[0].BestOpenableURL != "" {
		t.Fatalf("BestOpenableURL = %q, want empty for a resolved-only attempt", recent[0].BestOpenableURL)
	}
}

func TestMedia_WorldBoundaryObservationsNotMerged(t *testing.T) {
	m := NewManager()
	base := time.Now().UTC()

	applyOne(t, m, joiningObs("j1", "wrld_1", "inst_1", base))
	const url1 = "https://example.com/session1.mp4"
	applyOne(t, m, resourceURLObs(t, "o1",
		vrclog.RemoteResource{URL: url1, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		nil, "vrchat.core", base.Add(1*time.Second)))

	// Definitive world transition.
	applyOne(t, m, joiningObs("j2", "wrld_2", "inst_2", base.Add(2*time.Second)))

	// Same URL observed again in the new world session: because
	// currentSessionAttempts() scopes correlation to inst_2, this must NOT
	// merge into the inst_1 attempt even though the URL string matches.
	applyOne(t, m, resourceURLObs(t, "o2",
		vrclog.RemoteResource{URL: url1, Kind: vrclog.ResourceKindVideo, Role: vrclog.ResourceRoleSource},
		nil, "vrchat.core", base.Add(3*time.Second)))

	recent := m.RecentMedia(0)
	if len(recent) != 2 {
		t.Fatalf("RecentMedia = %d attempts, want 2 (recent history must be preserved across world transition, not merged): %+v", len(recent), recent)
	}
}
