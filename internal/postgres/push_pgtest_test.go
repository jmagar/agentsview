//go:build pgtest

package postgres

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

// TestPushSystemFingerprintCollisionRegression verifies that the fast-path
// in pushMessages correctly detects a change when the is_system flags are
// reclassified between two ordinal sets that previously collided under the
// two-component (SUM, SUM-of-squares) fingerprint: {0,4,5} and {1,2,6}
// both produce sum=9, sumSq=41.
//
// Steps:
//  1. Push a session with 7 messages where ordinals {0,4,5} are system.
//  2. Without changing content lengths, reclassify to {1,2,6} as system.
//  3. Push again with full=false.
//  4. Confirm PG now reflects the updated is_system values.
func TestPushSystemFingerprintCollisionRegression(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_push_sysfingerprint_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// Local SQLite DB.
	localDB, err := db.Open(
		filepath.Join(t.TempDir(), "local.db"),
	)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer localDB.Close()

	sync := &Sync{
		pg:      pg,
		local:   localDB,
		machine: "test-machine",
		schema:  schema,
		// Mark schema done so Push skips EnsureSchema.
		schemaDone: true,
	}

	const sessID = "fp-collision-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 7,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// First set: system ordinals {0,4,5}.
	firstSet := map[int]bool{0: true, 4: true, 5: true}
	msgs := make([]db.Message, 7)
	for i := range 7 {
		msgs[i] = db.Message{
			SessionID:     sessID,
			Ordinal:       i,
			Role:          "user",
			Content:       "x",
			ContentLength: 1,
			IsSystem:      firstSet[i],
		}
	}
	if err := localDB.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages (first set): %v", err)
	}

	// First push.
	_, err = sync.Push(ctx, false)
	if err != nil {
		t.Fatalf("Push (first): %v", err)
	}

	// Verify PG reflects system ordinals {0,4,5}.
	checkIsSystem(t, pg, sessID, firstSet, 7)

	// Switch to {1,2,6} — same sum(ordinal)=9, same sum(ordinal²)=41,
	// but the string fingerprint differs ("0,4,5" vs "1,2,6").
	// Replace local messages with updated is_system flags.
	secondSet := map[int]bool{1: true, 2: true, 6: true}
	for i := range 7 {
		msgs[i].IsSystem = secondSet[i]
	}
	if err := localDB.ReplaceSessionMessages(sessID, msgs); err != nil {
		t.Fatalf("ReplaceSessionMessages (second set): %v", err)
	}

	// Force re-evaluation by clearing both the watermark and the cached
	// session-level boundary fingerprints. The session-level fingerprint
	// does not include is_system flags (only metadata like MessageCount),
	// so the boundary cache must be cleared for the incremental push to
	// reach pushMessages and compare the message-level string fingerprint.
	if err := localDB.SetSyncState("last_push_at", ""); err != nil {
		t.Fatalf("clearing last_push_at: %v", err)
	}
	if err := localDB.SetSyncState(lastPushBoundaryStateKey, ""); err != nil {
		t.Fatalf("clearing boundary state: %v", err)
	}

	// Second push — must NOT skip due to fingerprint match.
	_, err = sync.Push(ctx, false)
	if err != nil {
		t.Fatalf("Push (second): %v", err)
	}

	// Verify PG now reflects updated system ordinals {1,2,6}.
	checkIsSystem(t, pg, sessID, secondSet, 7)
}

// TestPushZeroSessionsClearsStaleBackfillKeys verifies that a full push
// with zero local sessions still cleans up backfill_pending keys for
// machines that no longer have sessions in PG (e.g., after a machine
// rename). Without this cleanup, stale keys block pg serve forever.
func TestPushZeroSessionsClearsStaleBackfillKeys(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_stalekey_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// Insert a stale backfill_pending key for a machine that has no
	// sessions in PG (simulates a renamed machine).
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:old-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting stale key: %v", err)
	}

	// Local SQLite DB with zero sessions.
	localDB, err := db.Open(
		filepath.Join(t.TempDir(), "local.db"),
	)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer localDB.Close()

	sync := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "new-machine",
		schema:     schema,
		schemaDone: true,
	}

	// Full push with zero sessions should clean up the stale key.
	if _, err := sync.Push(ctx, true); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Verify the stale key was removed.
	var count int
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_metadata
		 WHERE key = 'backfill_pending:old-machine'`,
	).Scan(&count); err != nil {
		t.Fatalf("querying stale key: %v", err)
	}
	if count != 0 {
		t.Errorf("stale backfill_pending key still present; want 0, got %d", count)
	}
}

// TestPushZeroLocalSessionsOrphanedPG verifies that a full push with zero
// local sessions retains backfill_pending when PG contains orphaned
// sessions for the machine. Orphaned PG rows have stale is_system values
// that cannot be reconciled without local data, so pg serve must stay
// blocked until the orphaned data is resolved.
func TestPushZeroLocalSessionsOrphanedPG(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_orphan_zero_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	machine := "test-machine"

	// Insert a backfill_pending key for this machine using the
	// production value 'true' (IsBackfillPendingForMachine checks
	// v == "true").
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ($1, 'true')`,
		"backfill_pending:"+machine,
	); err != nil {
		t.Fatalf("inserting backfill key: %v", err)
	}

	// Insert an orphaned session in PG for this machine (no local
	// counterpart) with at least one message so it counts for
	// backfill coverage.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sessions (id, machine, project, agent, created_at, updated_at)
		 VALUES ('orphan-sess', $1, '/tmp/test', 'claude', now(), now())`,
		machine,
	); err != nil {
		t.Fatalf("inserting orphaned session: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO messages (session_id, ordinal, role, content, timestamp, content_length, is_system)
		 VALUES ('orphan-sess', 0, 'user', 'orphan msg', now(), 10, FALSE)`,
	); err != nil {
		t.Fatalf("inserting orphan message: %v", err)
	}

	// Local SQLite DB with zero sessions.
	localDB, err := db.Open(
		filepath.Join(t.TempDir(), "local.db"),
	)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer localDB.Close()

	sync := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    machine,
		schema:     schema,
		schemaDone: true,
	}

	// Full push with zero local sessions but orphaned PG rows.
	if _, err := sync.Push(ctx, true); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// backfill_pending must be retained because PG contains orphaned
	// sessions whose is_system values were not re-pushed.
	if !IsBackfillPendingForMachine(ctx, pg, machine) {
		t.Error("backfill_pending cleared; want retained " +
			"because orphaned PG sessions have stale is_system")
	}
}

// TestPushLocalSessionsPlusOrphanedPG verifies that a full push with
// local sessions retains backfill_pending when PG also contains
// orphaned sessions for the machine (no local counterpart). Orphaned
// rows have stale is_system values that were not re-pushed, so the
// flag must stay until the orphaned data is resolved.
func TestPushLocalSessionsPlusOrphanedPG(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_orphan_mixed_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	machine := "test-machine"

	// Insert a backfill_pending key using the production value 'true'.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ($1, 'true')`,
		"backfill_pending:"+machine,
	); err != nil {
		t.Fatalf("inserting backfill key: %v", err)
	}

	// Insert an orphaned session in PG (no local counterpart)
	// with a message so it counts for backfill coverage.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sessions (id, machine, project, agent, created_at, updated_at)
		 VALUES ('orphan-sess', $1, '/tmp/test', 'claude', now(), now())`,
		machine,
	); err != nil {
		t.Fatalf("inserting orphaned session: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO messages (session_id, ordinal, role, content, timestamp, content_length, is_system)
		 VALUES ('orphan-sess', 0, 'user', 'orphan msg', now(), 10, FALSE)`,
	); err != nil {
		t.Fatalf("inserting orphan message: %v", err)
	}

	// Local SQLite DB with one real session.
	localDB, err := db.Open(
		filepath.Join(t.TempDir(), "local.db"),
	)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer localDB.Close()

	sess := db.Session{
		ID:           "local-sess-001",
		Project:      "test-proj",
		Machine:      machine,
		Agent:        "claude",
		MessageCount: 1,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if err := localDB.InsertMessages([]db.Message{{
		SessionID:     sess.ID,
		Ordinal:       0,
		Role:          "user",
		Content:       "hello",
		ContentLength: 5,
	}}); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	sync := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    machine,
		schema:     schema,
		schemaDone: true,
	}

	// Full push with local sessions + orphaned PG rows.
	result, err := sync.Push(ctx, true)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if result.SessionsPushed != 1 {
		t.Errorf("SessionsPushed = %d; want 1", result.SessionsPushed)
	}
	if result.MessagesPushed < 1 {
		t.Errorf("MessagesPushed = %d; want >= 1", result.MessagesPushed)
	}

	// Verify the local session actually exists in PG.
	var pgSessCount int
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = $1`,
		"local-sess-001",
	).Scan(&pgSessCount); err != nil {
		t.Fatalf("querying pushed session: %v", err)
	}
	if pgSessCount != 1 {
		t.Errorf("local-sess-001 not found in PG; got %d row(s)", pgSessCount)
	}

	// backfill_pending must be retained because the orphaned PG
	// session was not re-pushed and has stale is_system values.
	if !IsBackfillPendingForMachine(ctx, pg, machine) {
		t.Error("backfill_pending cleared; want retained " +
			"because orphaned PG sessions have stale is_system")
	}
}

// checkIsSystem asserts that PG contains exactly wantTotal rows for the
// session with ordinals 0..wantTotal-1, and that each row's is_system
// matches wantSystem. Tracking the exact ordinal set prevents false
// positives from wrong-but-equal-count row sets.
func checkIsSystem(
	t *testing.T,
	pg *sql.DB,
	sessID string,
	wantSystem map[int]bool,
	wantTotal int,
) {
	t.Helper()
	rows, err := pg.Query(
		`SELECT ordinal, is_system FROM messages
		 WHERE session_id = $1 ORDER BY ordinal`,
		sessID,
	)
	if err != nil {
		t.Fatalf("querying PG messages: %v", err)
	}
	defer rows.Close()
	seen := make(map[int]bool, wantTotal)
	for rows.Next() {
		var ordinal int
		var isSystem bool
		if err := rows.Scan(&ordinal, &isSystem); err != nil {
			t.Fatalf("scanning row: %v", err)
		}
		seen[ordinal] = true
		want := wantSystem[ordinal]
		if isSystem != want {
			t.Errorf("ordinal %d: is_system=%v, want %v",
				ordinal, isSystem, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	if len(seen) != wantTotal {
		t.Errorf("PG has %d message rows for session %s, want %d",
			len(seen), sessID, wantTotal)
	}
	// Verify every expected ordinal was present (no gaps or substitutions).
	for i := range wantTotal {
		if !seen[i] {
			t.Errorf("ordinal %d missing from PG messages", i)
		}
	}
}
