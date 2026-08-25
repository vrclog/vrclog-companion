package store

import (
	"context"
	"fmt"
)

// CurrentSchemaVersion is the current database schema version, tracked via
// PRAGMA user_version. Version 2 is the Observation/Projector schema.
// Version 3 marks no table/column change but a stricter upstream vrclog-go
// payload contract (MediaTarget.Backend is now required): a version 2
// database may contain payloads that no longer decode, so it is rejected
// rather than silently failing during Projector rebuild. There is no
// automatic migration path from any earlier version.
const CurrentSchemaVersion = 3

// initSchema validates or creates the database schema.
//
//   - user_version == CurrentSchemaVersion: validate the expected tables
//     exist and use the database as-is.
//   - user_version == 0 and no legacy tables: fresh database, create schema.
//   - user_version == 0 with legacy tables, or any other version: fatal,
//     since this renewal does not support automatic migration.
func (s *Store) initSchema(ctx context.Context) error {
	version, err := s.userVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	switch version {
	case CurrentSchemaVersion:
		return s.validateSchema(ctx)
	case 0:
		hasLegacy, err := s.hasLegacyTables(ctx)
		if err != nil {
			return fmt.Errorf("check legacy tables: %w", err)
		}
		if hasLegacy {
			return fmt.Errorf(
				"%w: database at %s has schema version 0 with legacy tables from a pre-renewal build; "+
					"automatic migration is not supported — stop the app and rename or delete the database file to start fresh",
				ErrUnsupportedSchema, s.path,
			)
		}
		return s.createSchema(ctx)
	default:
		return fmt.Errorf(
			"%w: database at %s has schema version %d, expected %d; "+
				"automatic migration is not supported — stop the app and rename or delete the database file to start fresh",
			ErrUnsupportedSchema, s.path, version, CurrentSchemaVersion,
		)
	}
}

func (s *Store) userVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func (s *Store) hasLegacyTables(ctx context.Context) (bool, error) {
	const q = `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('events', 'ingest_cursor', 'parse_failures')`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	return found, rows.Err()
}

func (s *Store) createSchema(ctx context.Context) error {
	const schema = `
	CREATE TABLE observations (
		sequence       INTEGER PRIMARY KEY AUTOINCREMENT,
		id             TEXT NOT NULL UNIQUE,
		occurred_at    TEXT NOT NULL,
		type           TEXT NOT NULL,
		payload_json   TEXT NOT NULL,
		adapter_id     TEXT NOT NULL,
		rule_id        TEXT NOT NULL,
		record_id      TEXT NOT NULL,
		source_id      TEXT NOT NULL,
		source_offset  INTEGER NOT NULL,
		source_line    INTEGER NOT NULL,
		ingested_at    TEXT NOT NULL
	);

	CREATE INDEX observations_occurred_idx ON observations(occurred_at, sequence);
	CREATE INDEX observations_type_idx ON observations(type, sequence);
	CREATE INDEX observations_adapter_idx ON observations(adapter_id, sequence);
	CREATE INDEX observations_record_idx ON observations(record_id, sequence);

	CREATE TABLE ingest_cursors (
		source_id     TEXT PRIMARY KEY,
		path          TEXT NOT NULL,
		byte_offset   INTEGER NOT NULL,
		line_number   INTEGER NOT NULL,
		updated_at    TEXT NOT NULL
	);

	CREATE INDEX ingest_cursors_updated_idx ON ingest_cursors(updated_at);

	CREATE TABLE diagnostics (
		id             TEXT PRIMARY KEY,
		record_id      TEXT NOT NULL,
		source_id      TEXT NOT NULL,
		source_offset  INTEGER NOT NULL,
		source_line    INTEGER NOT NULL,
		adapter_id     TEXT,
		rule_id        TEXT,
		code           TEXT NOT NULL,
		message        TEXT NOT NULL,
		created_at     TEXT NOT NULL
	);

	CREATE INDEX diagnostics_created_idx ON diagnostics(created_at);

	CREATE TABLE metadata (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

func (s *Store) validateSchema(ctx context.Context) error {
	const q = `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('observations', 'ingest_cursors', 'diagnostics')`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()

	found := make(map[string]bool, 3)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, want := range []string{"observations", "ingest_cursors", "diagnostics"} {
		if !found[want] {
			return fmt.Errorf("%w: schema version %d at %s but missing table %q", ErrUnsupportedSchema, CurrentSchemaVersion, s.path, want)
		}
	}
	return nil
}
