package projector

import (
	"context"
	"iter"
	"sync"

	vrclog "github.com/vrclog/vrclog-go"

	"github.com/vrclog/vrclog-companion/internal/observation"
)

// Manager owns the World, Presence, and Media projectors and applies
// Observations to them in a fixed, documented order: World transition,
// then Presence reset, then Media session reset — so Media correlation
// always sees an up-to-date world session boundary within the same Apply
// call that triggered it.
type Manager struct {
	mu         sync.RWMutex
	world      *worldProjector
	presence   *presenceProjector
	media      *mediaProjector
	rebuilding bool
}

// NewManager creates an empty Manager. Call Rebuild once at startup with
// all persisted Observations before serving any live Apply calls.
func NewManager() *Manager {
	return &Manager{
		world:    newWorldProjector(),
		presence: newPresenceProjector(),
		media:    newMediaProjector(),
	}
}

// Rebuild replays observations in sequence order to reconstruct current
// state deterministically. It replays into fresh World/Presence/Media
// projectors and only swaps them into m once replay succeeds — a failed
// or interrupted Rebuild (decode error, ctx cancellation) leaves any
// previously-served state untouched, and calling Rebuild a second time
// (e.g. a manual re-rebuild) always starts from empty projector state
// rather than replaying on top of whatever was already there, so repeated
// Rebuild calls with the same input are idempotent. Change emission is
// suppressed for the duration of Rebuild — callers must not fan Rebuild's
// (nil) results out to SSE/notifications.
func (m *Manager) Rebuild(ctx context.Context, observations iter.Seq2[observation.StoredObservation, error]) error {
	scratch := &Manager{
		world:      newWorldProjector(),
		presence:   newPresenceProjector(),
		media:      newMediaProjector(),
		rebuilding: true,
	}

	for obs, err := range observations {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := scratch.applyLocked(obs); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.world = scratch.world
	m.presence = scratch.presence
	m.media = scratch.media
	return nil
}

// Apply decodes obs's canonical Event and applies it to the appropriate
// Projector(s), returning the Changes produced. Must only be called for
// newly inserted, live Observations — never for rebuild or duplicate
// replay.
func (m *Manager) Apply(obs observation.StoredObservation) ([]Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked(obs)
}

func (m *Manager) applyLocked(obs observation.StoredObservation) ([]Change, error) {
	event, err := obs.DecodeEvent()
	if err != nil {
		return nil, err
	}

	var changes []Change

	switch ev := event.(type) {
	case vrclog.WorldJoiningObserved:
		wc := m.world.applyJoining(ev, obs.OccurredAt)
		changes = append(changes, wc...)
		if isDefinitiveTransition(wc) {
			m.presence.reset()
			m.media.resetSession(ev.World.InstanceID)
		}
	case vrclog.WorldEnteringObserved:
		changes = append(changes, m.world.applyEntering(ev, obs.OccurredAt)...)
	case vrclog.PlayerJoined:
		changes = append(changes, m.presence.applyJoined(ev, obs.OccurredAt, string(obs.ID))...)
	case vrclog.PlayerLeft:
		changes = append(changes, m.presence.applyLeft(ev, obs.OccurredAt)...)
	case vrclog.ResourceURLObserved:
		changes = append(changes, m.media.applyResourceURL(ev, obs)...)
	case vrclog.ResourceResolved:
		changes = append(changes, m.media.applyResourceResolved(ev, obs)...)
	case vrclog.MediaErrorObserved:
		changes = append(changes, m.media.applyMediaError(ev, obs)...)
	case vrclog.AdapterEvent:
		// Extension envelope for adapter-specific data that doesn't map to
		// a canonical World/Presence/Media event; intentionally not
		// projected.
	}

	if m.rebuilding {
		return nil, nil
	}
	return changes, nil
}

func isDefinitiveTransition(changes []Change) bool {
	for _, c := range changes {
		if _, ok := c.(WorldChanged); ok {
			return true
		}
	}
	return false
}

// Snapshot returns a thread-safe, point-in-time read of all Projector
// state.
func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{
		World:               m.world.currentCopy(),
		Players:             m.presence.snapshot(),
		LatestOpenableMedia: m.media.latestOpenable(),
	}
}

// RecentMedia returns up to limit recent MediaAttempts, newest first. A
// non-positive limit returns all retained attempts (at most mediaMaxRecent).
func (m *Manager) RecentMedia(limit int) []*MediaAttempt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.media.recentSnapshot()
	if limit <= 0 || limit >= len(all) {
		return all
	}
	return all[:limit]
}
