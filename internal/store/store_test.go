package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	vrclog "github.com/vrclog/vrclog-go"

	"github.com/vrclog/vrclog-companion/internal/observation"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeRecord(sourceID string, offset int64, line uint64, at time.Time) vrclog.Record {
	return vrclog.Record{
		ID:         vrclog.RecordID(fmt.Sprintf("rec-%s-%d", sourceID, offset)),
		Time:       at,
		SourceID:   vrclog.SourceID(sourceID),
		Path:       "/tmp/" + sourceID + ".txt",
		Offset:     offset,
		NextOffset: offset + 100,
		Line:       line,
	}
}

func makePlayerJoined(id string, record vrclog.Record, name string) vrclog.Observation {
	return vrclog.Observation{
		ID:        vrclog.ObservationID(id),
		Time:      record.Time,
		AdapterID: "vrchat.core",
		RuleID:    "player_joined",
		Record: vrclog.RecordRef{
			ID: record.ID, SourceID: record.SourceID, Offset: record.Offset, Line: record.Line,
		},
		Event: vrclog.PlayerJoined{Player: vrclog.Player{ID: "usr_" + name, DisplayName: name}},
	}
}

func makePlayerLeft(id string, record vrclog.Record, name string) vrclog.Observation {
	return vrclog.Observation{
		ID:        vrclog.ObservationID(id),
		Time:      record.Time,
		AdapterID: "vrchat.core",
		RuleID:    "player_left",
		Record: vrclog.RecordRef{
			ID: record.ID, SourceID: record.SourceID, Offset: record.Offset, Line: record.Line,
		},
		Event: vrclog.PlayerLeft{Player: vrclog.Player{ID: "usr_" + name, DisplayName: name}},
	}
}

// --- 23.1 Schema tests ---

func TestSchema_EmptyDBCreatesVersion3(t *testing.T) {
	s := openTestStore(t)
	v, err := s.userVersion(context.Background())
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, CurrentSchemaVersion)
	}
}

func TestSchema_Version3Opens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen existing v3 db: %v", err)
	}
	s2.Close()
}

func TestSchema_Version1Rejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	raw.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedSchema", err)
	}
}

// TestSchema_Version2Rejects guards the vrclog-go dependency bump that made
// MediaTarget.Backend required: a version 2 database may hold observation
// payloads encoded under the old, more permissive contract that no longer
// decode, so it must be rejected up front rather than failing later during
// Projector rebuild.
func TestSchema_Version2Rejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	raw.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedSchema", err)
	}
}

func TestSchema_Version0WithLegacyTablesRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")

	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	raw.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("Open() error = %v, want ErrUnsupportedSchema", err)
	}
}

func TestSchema_WALAndConstraintsEnabled(t *testing.T) {
	s := openTestStore(t)
	mode, err := s.journalMode()
	if err != nil {
		t.Fatalf("journalMode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// --- 23.2 CommitRecord atomicity tests ---

func TestCommitRecord_ObservationAndCursorCommit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec, "Alice")

	result, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec,
		Result:     vrclog.Result{Observations: []vrclog.Observation{obs}},
		IngestedAt: now,
	})
	if err != nil {
		t.Fatalf("CommitRecord: %v", err)
	}
	if len(result.InsertedObservations) != 1 {
		t.Fatalf("InsertedObservations = %d, want 1", len(result.InsertedObservations))
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor == nil || cursor.Offset != rec.NextOffset {
		t.Fatalf("cursor = %+v, want offset %d", cursor, rec.NextOffset)
	}
}

func TestCommitRecord_ZeroObservationsStillCommitsCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	result, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec,
		Result:     vrclog.Result{},
		IngestedAt: now,
	})
	if err != nil {
		t.Fatalf("CommitRecord: %v", err)
	}
	if len(result.InsertedObservations) != 0 {
		t.Fatalf("InsertedObservations = %d, want 0", len(result.InsertedObservations))
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor == nil || cursor.Offset != rec.NextOffset {
		t.Fatalf("cursor = %+v, want offset %d", cursor, rec.NextOffset)
	}
}

func TestCommitRecord_DiagnosticAndCursorCommit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	diag := vrclog.Diagnostic{
		Code:    vrclog.DiagnosticAdapterError,
		Message: "boom",
		Record:  vrclog.RecordRef{ID: rec.ID, SourceID: rec.SourceID, Offset: rec.Offset, Line: rec.Line},
	}

	_, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec,
		Result:     vrclog.Result{Diagnostics: []vrclog.Diagnostic{diag}},
		IngestedAt: now,
	})
	if err != nil {
		t.Fatalf("CommitRecord: %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM diagnostics").Scan(&count); err != nil {
		t.Fatalf("count diagnostics: %v", err)
	}
	if count != 1 {
		t.Fatalf("diagnostics count = %d, want 1", count)
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor == nil || cursor.Offset != rec.NextOffset {
		t.Fatalf("cursor not advanced: %+v", cursor)
	}
}

func TestCommitRecord_DuplicateIdenticalObservationIgnoredCursorAdvances(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec1 := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec1, "Alice")

	if _, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec1,
		Result:     vrclog.Result{Observations: []vrclog.Observation{obs}},
		IngestedAt: now,
	}); err != nil {
		t.Fatalf("first CommitRecord: %v", err)
	}

	// Replay: same observation content, later Record (simulates re-reading
	// after a crash before the cursor advanced further).
	rec2 := makeRecord("src1", rec1.NextOffset, 2, now)
	obsReplay := makePlayerJoined("obs1", rec1, "Alice") // same ID, same fields

	result, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec2,
		Result:     vrclog.Result{Observations: []vrclog.Observation{obsReplay}},
		IngestedAt: now,
	})
	if err != nil {
		t.Fatalf("replay CommitRecord: %v", err)
	}
	if len(result.InsertedObservations) != 0 {
		t.Fatalf("InsertedObservations = %d, want 0 (duplicate)", len(result.InsertedObservations))
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor.Offset != rec2.NextOffset {
		t.Fatalf("cursor.Offset = %d, want %d (cursor must still advance on duplicate)", cursor.Offset, rec2.NextOffset)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations").Scan(&count); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if count != 1 {
		t.Fatalf("observations count = %d, want 1 (no duplicate row)", count)
	}
}

func TestCommitRecord_DuplicateConflictingObservationRollsBack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec1 := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec1, "Alice")
	if _, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec1,
		Result:     vrclog.Result{Observations: []vrclog.Observation{obs}},
		IngestedAt: now,
	}); err != nil {
		t.Fatalf("first CommitRecord: %v", err)
	}

	// Same Observation ID, different payload (different player name) -> conflict.
	rec2 := makeRecord("src1", rec1.NextOffset, 2, now)
	conflicting := makePlayerJoined("obs1", rec1, "Bob")
	second := makePlayerJoined("obs2", rec2, "Carol")

	_, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec2,
		Result:     vrclog.Result{Observations: []vrclog.Observation{second, conflicting}},
		IngestedAt: now,
	})
	if !errors.Is(err, ErrObservationConflict) {
		t.Fatalf("CommitRecord error = %v, want ErrObservationConflict", err)
	}

	// Whole transaction must have rolled back: "second" (obs2) must not
	// exist, and the cursor must remain at rec1's position.
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM observations WHERE id = 'obs2'").Scan(&count); err != nil {
		t.Fatalf("count obs2: %v", err)
	}
	if count != 0 {
		t.Fatalf("obs2 should not exist after rollback, found %d", count)
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor.Offset != rec1.NextOffset {
		t.Fatalf("cursor.Offset = %d, want %d (must not advance on rollback)", cursor.Offset, rec1.NextOffset)
	}
}

func TestCommitRecord_InsertedListOrderAndContents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	obs1 := makePlayerJoined("obs1", rec, "Alice")
	obs2 := makePlayerJoined("obs2", rec, "Bob")
	obs3 := makePlayerLeft("obs3", rec, "Alice")

	result, err := s.CommitRecord(ctx, RecordCommit{
		Record:     rec,
		Result:     vrclog.Result{Observations: []vrclog.Observation{obs1, obs2, obs3}},
		IngestedAt: now,
	})
	if err != nil {
		t.Fatalf("CommitRecord: %v", err)
	}
	if len(result.InsertedObservations) != 3 {
		t.Fatalf("InsertedObservations = %d, want 3", len(result.InsertedObservations))
	}
	wantIDs := []string{"obs1", "obs2", "obs3"}
	for i, want := range wantIDs {
		if string(result.InsertedObservations[i].ID) != want {
			t.Fatalf("InsertedObservations[%d].ID = %s, want %s", i, result.InsertedObservations[i].ID, want)
		}
	}
}

// --- 23.3 Cursor tests ---

func TestLatestCursor_ReturnsMostRecentlyUpdated(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec1 := makeRecord("src1", 0, 1, now)
	if _, err := s.CommitRecord(ctx, RecordCommit{Record: rec1, IngestedAt: now}); err != nil {
		t.Fatalf("commit src1: %v", err)
	}

	rec2 := makeRecord("src2", 0, 1, now.Add(time.Second))
	if _, err := s.CommitRecord(ctx, RecordCommit{Record: rec2, IngestedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("commit src2: %v", err)
	}

	cursor, err := s.LatestCursor(ctx)
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor.SourceID != "src2" {
		t.Fatalf("LatestCursor.SourceID = %s, want src2", cursor.SourceID)
	}
}

func TestCursor_RotationProducesMultipleSourceRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := s.CommitRecord(ctx, RecordCommit{Record: makeRecord("src1", 0, 1, now), IngestedAt: now}); err != nil {
		t.Fatalf("commit src1: %v", err)
	}
	if _, err := s.CommitRecord(ctx, RecordCommit{Record: makeRecord("src2", 0, 1, now), IngestedAt: now}); err != nil {
		t.Fatalf("commit src2: %v", err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ingest_cursors").Scan(&count); err != nil {
		t.Fatalf("count cursors: %v", err)
	}
	if count != 2 {
		t.Fatalf("ingest_cursors rows = %d, want 2", count)
	}
}

// --- 23.4 Query tests ---

func TestListObservations_SequencePagination(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		rec := makeRecord("src1", int64(i*10), uint64(i+1), now.Add(time.Duration(i)*time.Second))
		obs := makePlayerJoined(fmt.Sprintf("obs%d", i), rec, fmt.Sprintf("Player%d", i))
		if _, err := s.CommitRecord(ctx, RecordCommit{
			Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
		}); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	page1, next1, err := s.ListObservations(ctx, ObservationQuery{Limit: 2, Order: OrderAsc})
	if err != nil {
		t.Fatalf("ListObservations page1: %v", err)
	}
	if len(page1) != 2 || next1 == nil {
		t.Fatalf("page1 = %d items, next=%v, want 2 items with next cursor", len(page1), next1)
	}
	if string(page1[0].ID) != "obs0" || string(page1[1].ID) != "obs1" {
		t.Fatalf("page1 IDs = %s, %s, want obs0, obs1", page1[0].ID, page1[1].ID)
	}

	page2, _, err := s.ListObservations(ctx, ObservationQuery{Limit: 2, Order: OrderAsc, AfterSequence: next1})
	if err != nil {
		t.Fatalf("ListObservations page2: %v", err)
	}
	if len(page2) != 2 || string(page2[0].ID) != "obs2" {
		t.Fatalf("page2 = %+v, want [obs2, obs3]", page2)
	}
}

func TestListObservations_TypeFilterArbitraryExact(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec, "Alice")
	if _, err := s.CommitRecord(ctx, RecordCommit{
		Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	unknownType := "some.unknown.kind"
	items, _, err := s.ListObservations(ctx, ObservationQuery{Type: &unknownType})
	if err != nil {
		t.Fatalf("ListObservations unknown type: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("unknown type filter returned %d items, want 0 (no allowlist rejection)", len(items))
	}

	knownType := string(vrclog.EventKindPlayerJoined)
	items, _, err = s.ListObservations(ctx, ObservationQuery{Type: &knownType})
	if err != nil {
		t.Fatalf("ListObservations known type: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("known type filter returned %d items, want 1", len(items))
	}
}

func TestListObservations_AdapterFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec, "Alice")
	if _, err := s.CommitRecord(ctx, RecordCommit{
		Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	other := "community.other"
	items, _, err := s.ListObservations(ctx, ObservationQuery{AdapterID: &other})
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items for unrelated adapter, want 0", len(items))
	}

	core := "vrchat.core"
	items, _, err = s.ListObservations(ctx, ObservationQuery{AdapterID: &core})
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items for vrchat.core, want 1", len(items))
	}
}

func TestListObservations_SinceUntil(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 3; i++ {
		rec := makeRecord("src1", int64(i*10), uint64(i+1), base.Add(time.Duration(i)*time.Hour))
		obs := makePlayerJoined(fmt.Sprintf("obs%d", i), rec, fmt.Sprintf("P%d", i))
		if _, err := s.CommitRecord(ctx, RecordCommit{
			Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: base,
		}); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	since := base.Add(30 * time.Minute)
	until := base.Add(90 * time.Minute)
	items, _, err := s.ListObservations(ctx, ObservationQuery{Since: &since, Until: &until, Order: OrderAsc})
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(items) != 1 || string(items[0].ID) != "obs1" {
		t.Fatalf("since/until filter = %+v, want only obs1", items)
	}
}

func TestListObservations_DeterministicOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		rec := makeRecord("src1", int64(i*10), uint64(i+1), now)
		obs := makePlayerJoined(fmt.Sprintf("obs%d", i), rec, fmt.Sprintf("P%d", i))
		if _, err := s.CommitRecord(ctx, RecordCommit{
			Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
		}); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	desc, _, err := s.ListObservations(ctx, ObservationQuery{Order: OrderDesc})
	if err != nil {
		t.Fatalf("ListObservations desc: %v", err)
	}
	if len(desc) != 3 || string(desc[0].ID) != "obs2" || string(desc[2].ID) != "obs0" {
		t.Fatalf("desc order = %+v, want obs2,obs1,obs0", desc)
	}
}

func TestObservationByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	rec := makeRecord("src1", 0, 1, now)
	obs := makePlayerJoined("obs1", rec, "Alice")
	if _, err := s.CommitRecord(ctx, RecordCommit{
		Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	found, err := s.ObservationByID(ctx, "obs1")
	if err != nil {
		t.Fatalf("ObservationByID: %v", err)
	}
	if found == nil {
		t.Fatal("ObservationByID returned nil, want a match")
	}

	notFound, err := s.ObservationByID(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("ObservationByID: %v", err)
	}
	if notFound != nil {
		t.Fatal("ObservationByID should return nil for unknown ID")
	}
}

func TestObservationsAfterSequence_SSEBacklog(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	var seqs []int64
	for i := 0; i < 4; i++ {
		rec := makeRecord("src1", int64(i*10), uint64(i+1), now)
		obs := makePlayerJoined(fmt.Sprintf("obs%d", i), rec, fmt.Sprintf("P%d", i))
		result, err := s.CommitRecord(ctx, RecordCommit{
			Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
		})
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		seqs = append(seqs, result.InsertedObservations[0].Sequence)
	}

	backlog, err := s.ObservationsAfterSequence(ctx, seqs[1], 10)
	if err != nil {
		t.Fatalf("ObservationsAfterSequence: %v", err)
	}
	if len(backlog) != 2 || string(backlog[0].ID) != "obs2" || string(backlog[1].ID) != "obs3" {
		t.Fatalf("backlog = %+v, want [obs2, obs3]", backlog)
	}
}

func TestAllObservations_SequenceAscendingForRebuild(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		rec := makeRecord("src1", int64(i*10), uint64(i+1), now)
		obs := makePlayerJoined(fmt.Sprintf("obs%d", i), rec, fmt.Sprintf("P%d", i))
		if _, err := s.CommitRecord(ctx, RecordCommit{
			Record: rec, Result: vrclog.Result{Observations: []vrclog.Observation{obs}}, IngestedAt: now,
		}); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	var got []observation.StoredObservation
	for obs, err := range s.AllObservations(ctx) {
		if err != nil {
			t.Fatalf("AllObservations: %v", err)
		}
		got = append(got, obs)
	}
	if len(got) != 3 {
		t.Fatalf("AllObservations returned %d items, want 3", len(got))
	}
	for i, want := range []string{"obs0", "obs1", "obs2"} {
		if string(got[i].ID) != want {
			t.Fatalf("got[%d].ID = %s, want %s", i, got[i].ID, want)
		}
	}
}
