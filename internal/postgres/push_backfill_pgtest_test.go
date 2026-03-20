//go:build pgtest

package postgres

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wesm/agentsview/internal/db"
)

// TestEnsureSchemaBackfillPendingPerMachine verifies that
// EnsureSchema sets a backfill_pending flag for each machine
// that has existing sessions when upgrading from schema v1.
func TestEnsureSchemaBackfillPendingPerMachine(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_ensure_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}

	// Create tables at version 2.
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema (initial): %v", err)
	}

	// Downgrade schema_version to 1.
	if _, err := pg.ExecContext(ctx,
		`UPDATE sync_metadata SET value = '1'
		 WHERE key = 'schema_version'`,
	); err != nil {
		t.Fatalf("downgrading schema_version: %v", err)
	}

	// Insert sessions for two machines directly in PG, each
	// with a message so they qualify for backfill.
	for _, m := range []string{"machine-a", "machine-b"} {
		sessID := "sess-" + m
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO sessions
			 (id, machine, project, agent, created_at, updated_at)
			 VALUES ($1, $2, 'proj', 'claude', now(), now())`,
			sessID, m,
		); err != nil {
			t.Fatalf("inserting session for %s: %v", m, err)
		}
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO messages
			 (session_id, ordinal, role, content, timestamp,
			  content_length, is_system)
			 VALUES ($1, 0, 'user', 'msg', now(), 3, FALSE)`,
			sessID,
		); err != nil {
			t.Fatalf("inserting message for %s: %v", m, err)
		}
	}

	// Re-run EnsureSchema — should detect v1→v2 upgrade and
	// set backfill_pending for both machines.
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema (upgrade): %v", err)
	}

	// Verify backfill_pending flags.
	for _, m := range []string{"machine-a", "machine-b"} {
		var count int
		key := "backfill_pending:" + m
		if err := pg.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sync_metadata
			 WHERE key = $1`, key,
		).Scan(&count); err != nil {
			t.Fatalf("querying %s: %v", key, err)
		}
		if count != 1 {
			t.Errorf("%s: want 1 row, got %d", key, count)
		}
	}

	// Verify schema_version is back to 2.
	ver, err := GetSchemaVersion(ctx, pg)
	if err != nil {
		t.Fatalf("GetSchemaVersion: %v", err)
	}
	if ver != SchemaVersion {
		t.Errorf("schema_version = %d; want %d",
			ver, SchemaVersion)
	}
}

// TestPushDetectsSchemaUpgradeForcesFull verifies that Push
// auto-detects a schema upgrade (schemaDone=false, PG version
// < SchemaVersion) and forces a full push to backfill
// is_system values.
func TestPushDetectsSchemaUpgradeForcesFull(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_upgrade_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

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
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}

	const sessID = "upgrade-sess-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 2,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// Initial messages: all is_system=false.
	msgs := []db.Message{
		{
			SessionID: sessID, Ordinal: 0,
			Role: "user", Content: "hello",
			ContentLength: 5, IsSystem: false,
		},
		{
			SessionID: sessID, Ordinal: 1,
			Role: "assistant", Content: "world",
			ContentLength: 5, IsSystem: false,
		},
	}
	if err := localDB.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// First push — establishes baseline in PG.
	if _, err := sync.Push(ctx, false); err != nil {
		t.Fatalf("Push (initial): %v", err)
	}

	// Downgrade schema_version to 1 in PG.
	if _, err := pg.ExecContext(ctx,
		`UPDATE sync_metadata SET value = '1'
		 WHERE key = 'schema_version'`,
	); err != nil {
		t.Fatalf("downgrading schema_version: %v", err)
	}

	// Update local: mark ordinal 0 as is_system=true.
	msgs[0].IsSystem = true
	if err := localDB.ReplaceSessionMessages(
		sessID, msgs,
	); err != nil {
		t.Fatalf("ReplaceSessionMessages: %v", err)
	}

	// Create a new Sync with schemaDone=false so Push checks
	// the PG schema version.
	sync2 := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "test-machine",
		schema:     schema,
		schemaDone: false,
	}

	// Push(false) — should auto-detect upgrade and force full.
	if _, err := sync2.Push(ctx, false); err != nil {
		t.Fatalf("Push (upgrade): %v", err)
	}

	// Verify PG reflects updated is_system values.
	wantSystem := map[int]bool{0: true}
	checkIsSystem(t, pg, sessID, wantSystem, 2)

	// Verify schema_version is now 2.
	ver, err := GetSchemaVersion(ctx, pg)
	if err != nil {
		t.Fatalf("GetSchemaVersion: %v", err)
	}
	if ver != SchemaVersion {
		t.Errorf("schema_version = %d; want %d",
			ver, SchemaVersion)
	}
}

// TestPushBackfillPendingForcesFull verifies that Push detects
// a backfill_pending flag for the current machine and forces a
// full push, even when schemaDone=true.
func TestPushBackfillPendingForcesFull(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_pending_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

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
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}

	const sessID = "backfill-pend-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 2,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	msgs := []db.Message{
		{
			SessionID: sessID, Ordinal: 0,
			Role: "user", Content: "hello",
			ContentLength: 5, IsSystem: false,
		},
		{
			SessionID: sessID, Ordinal: 1,
			Role: "assistant", Content: "world",
			ContentLength: 5, IsSystem: false,
		},
	}
	if err := localDB.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// Initial push.
	if _, err := sync.Push(ctx, false); err != nil {
		t.Fatalf("Push (initial): %v", err)
	}

	// Manually set backfill_pending for this machine.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:test-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill_pending: %v", err)
	}

	// Update local: mark ordinal 0 as is_system=true.
	msgs[0].IsSystem = true
	if err := localDB.ReplaceSessionMessages(
		sessID, msgs,
	); err != nil {
		t.Fatalf("ReplaceSessionMessages: %v", err)
	}

	// Clear watermark and boundary state so session is
	// re-evaluated.
	if err := localDB.SetSyncState(
		"last_push_at", "",
	); err != nil {
		t.Fatalf("clearing last_push_at: %v", err)
	}
	if err := localDB.SetSyncState(
		lastPushBoundaryStateKey, "",
	); err != nil {
		t.Fatalf("clearing boundary state: %v", err)
	}

	// Push(false) with schemaDone=true — should detect
	// backfill_pending and force full.
	sync2 := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}
	if _, err := sync2.Push(ctx, false); err != nil {
		t.Fatalf("Push (backfill): %v", err)
	}

	// Verify PG reflects updated is_system values.
	wantSystem := map[int]bool{0: true}
	checkIsSystem(t, pg, sessID, wantSystem, 2)

	// Verify backfill_pending is cleared.
	var count int
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_metadata
		 WHERE key = 'backfill_pending:test-machine'`,
	).Scan(&count); err != nil {
		t.Fatalf("querying backfill_pending: %v", err)
	}
	if count != 0 {
		t.Errorf("backfill_pending still present; "+
			"want 0, got %d", count)
	}
}

// TestPushFullBackfillUpdatesIsSystemWithoutContentChange
// verifies that a full push (triggered by backfill_pending)
// updates is_system even when the content fingerprint
// (sum/max/min of content_length) is unchanged. Only full=true
// bypasses the fast-path skip in pushMessages.
func TestPushFullBackfillUpdatesIsSystemWithoutContentChange(
	t *testing.T,
) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_nocontchg_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

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
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}

	const sessID = "nocontchg-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 3,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// Three messages with content lengths 10, 20, 30 — all
	// is_system=false.
	msgs := []db.Message{
		{
			SessionID: sessID, Ordinal: 0,
			Role:          "user",
			Content:       strings.Repeat("a", 10),
			ContentLength: 10, IsSystem: false,
		},
		{
			SessionID: sessID, Ordinal: 1,
			Role:          "assistant",
			Content:       strings.Repeat("b", 20),
			ContentLength: 20, IsSystem: false,
		},
		{
			SessionID: sessID, Ordinal: 2,
			Role:          "user",
			Content:       strings.Repeat("c", 30),
			ContentLength: 30, IsSystem: false,
		},
	}
	if err := localDB.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// Initial push.
	if _, err := sync.Push(ctx, false); err != nil {
		t.Fatalf("Push (initial): %v", err)
	}

	// Verify all is_system=false in PG.
	checkIsSystem(t, pg, sessID, map[int]bool{}, 3)

	// Set backfill_pending flag.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:test-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill_pending: %v", err)
	}

	// Update local: mark ordinal 0 as is_system=true.
	// Content and content_length are UNCHANGED — the content
	// fingerprint (sum=60, max=30, min=10) stays the same.
	msgs[0].IsSystem = true
	if err := localDB.ReplaceSessionMessages(
		sessID, msgs,
	); err != nil {
		t.Fatalf("ReplaceSessionMessages: %v", err)
	}

	// Clear watermark so session is re-evaluated.
	if err := localDB.SetSyncState(
		"last_push_at", "",
	); err != nil {
		t.Fatalf("clearing last_push_at: %v", err)
	}
	if err := localDB.SetSyncState(
		lastPushBoundaryStateKey, "",
	); err != nil {
		t.Fatalf("clearing boundary state: %v", err)
	}

	// Push(false) — backfill_pending forces full=true,
	// bypassing the content fingerprint fast-path.
	sync2 := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}
	if _, err := sync2.Push(ctx, false); err != nil {
		t.Fatalf("Push (backfill): %v", err)
	}

	// Verify PG ordinal 0 has is_system=true.
	wantSystem := map[int]bool{0: true}
	checkIsSystem(t, pg, sessID, wantSystem, 3)
}

// TestPushBackfillClearNotResetOnNextPush verifies that once
// backfill_pending is cleared by a full push, a subsequent
// incremental push does not re-create the flag.
func TestPushBackfillClearNotResetOnNextPush(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_noreset_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	localDB, err := db.Open(
		filepath.Join(t.TempDir(), "local.db"),
	)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer localDB.Close()

	const sessID = "noreset-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 1,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if err := localDB.InsertMessages([]db.Message{{
		SessionID: sessID, Ordinal: 0,
		Role: "user", Content: "hello",
		ContentLength: 5,
	}}); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	sync := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}

	// Set backfill_pending, then full push to clear it.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:test-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill_pending: %v", err)
	}

	if _, err := sync.Push(ctx, true); err != nil {
		t.Fatalf("Push (full): %v", err)
	}

	// Verify cleared.
	var count int
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_metadata
		 WHERE key = 'backfill_pending:test-machine'`,
	).Scan(&count); err != nil {
		t.Fatalf("querying backfill_pending: %v", err)
	}
	if count != 0 {
		t.Fatalf("backfill_pending not cleared after full push")
	}

	// Add a new message locally and do an incremental push.
	sess.MessageCount = 2
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession (update): %v", err)
	}
	if err := localDB.InsertMessages([]db.Message{{
		SessionID: sessID, Ordinal: 1,
		Role: "assistant", Content: "world",
		ContentLength: 5,
	}}); err != nil {
		t.Fatalf("InsertMessages (second): %v", err)
	}

	// Clear watermark so the incremental push picks up the
	// session.
	if err := localDB.SetSyncState(
		"last_push_at", "",
	); err != nil {
		t.Fatalf("clearing last_push_at: %v", err)
	}
	if err := localDB.SetSyncState(
		lastPushBoundaryStateKey, "",
	); err != nil {
		t.Fatalf("clearing boundary state: %v", err)
	}

	if _, err := sync.Push(ctx, false); err != nil {
		t.Fatalf("Push (incremental): %v", err)
	}

	// Verify backfill_pending is still NOT set.
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_metadata
		 WHERE key = 'backfill_pending:test-machine'`,
	).Scan(&count); err != nil {
		t.Fatalf("querying backfill_pending: %v", err)
	}
	if count != 0 {
		t.Errorf("backfill_pending re-created after "+
			"incremental push; want 0, got %d", count)
	}
}

// TestIsBackfillPendingIgnoresRetiredMachines verifies that
// IsBackfillPending (global) filters out keys for machines
// with no sessions, while IsBackfillPendingForMachine does
// not filter.
func TestIsBackfillPendingIgnoresRetiredMachines(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_retired_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// Insert backfill_pending for a machine with no sessions.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:retired-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill key: %v", err)
	}

	// Global check should return false (no sessions).
	if IsBackfillPending(ctx, pg) {
		t.Error("IsBackfillPending = true; want false " +
			"(no sessions for retired-machine)")
	}

	// Per-machine check should return true (does not filter).
	if !IsBackfillPendingForMachine(
		ctx, pg, "retired-machine",
	) {
		t.Error("IsBackfillPendingForMachine = false; " +
			"want true")
	}

	// Insert a session with a message for retired-machine.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, machine, project, agent, created_at, updated_at)
		 VALUES ('ret-sess', 'retired-machine', 'proj',
		         'claude', now(), now())`,
	); err != nil {
		t.Fatalf("inserting session: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO messages
		 (session_id, ordinal, role, content, timestamp,
		  content_length, is_system)
		 VALUES ('ret-sess', 0, 'user', 'msg', now(), 3, FALSE)`,
	); err != nil {
		t.Fatalf("inserting message: %v", err)
	}

	// Global check should now return true.
	if !IsBackfillPending(ctx, pg) {
		t.Error("IsBackfillPending = false; want true " +
			"(session with messages exists)")
	}

	// Delete the session (cascade deletes messages).
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM messages WHERE session_id = 'ret-sess'`,
	); err != nil {
		t.Fatalf("deleting messages: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = 'ret-sess'`,
	); err != nil {
		t.Fatalf("deleting session: %v", err)
	}

	// Global check should return false again.
	if IsBackfillPending(ctx, pg) {
		t.Error("IsBackfillPending = true; want false " +
			"(session deleted)")
	}

	// Per-machine check should still return true.
	if !IsBackfillPendingForMachine(
		ctx, pg, "retired-machine",
	) {
		t.Error("IsBackfillPendingForMachine = false; " +
			"want true (flag still set)")
	}
}

// TestMultiMachineIndependentBackfillStates verifies that
// backfill_pending flags are independent per machine: clearing
// one machine's flag does not affect another's.
func TestMultiMachineIndependentBackfillStates(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_multi_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// Two local DBs, one per machine.
	localA, err := db.Open(
		filepath.Join(t.TempDir(), "local-a.db"),
	)
	if err != nil {
		t.Fatalf("db.Open (a): %v", err)
	}
	defer localA.Close()

	localB, err := db.Open(
		filepath.Join(t.TempDir(), "local-b.db"),
	)
	if err != nil {
		t.Fatalf("db.Open (b): %v", err)
	}
	defer localB.Close()

	syncA := &Sync{
		pg:         pg,
		local:      localA,
		machine:    "machine-a",
		schema:     schema,
		schemaDone: true,
	}
	syncB := &Sync{
		pg:         pg,
		local:      localB,
		machine:    "machine-b",
		schema:     schema,
		schemaDone: true,
	}

	// Insert sessions and messages for each machine.
	for _, tc := range []struct {
		sync  *Sync
		local *db.DB
		id    string
		mach  string
	}{
		{syncA, localA, "multi-a-001", "machine-a"},
		{syncB, localB, "multi-b-001", "machine-b"},
	} {
		sess := db.Session{
			ID:           tc.id,
			Project:      "test-proj",
			Machine:      tc.mach,
			Agent:        "claude",
			MessageCount: 1,
			CreatedAt:    "2026-01-01T00:00:00Z",
		}
		if err := tc.local.UpsertSession(sess); err != nil {
			t.Fatalf("UpsertSession (%s): %v", tc.mach, err)
		}
		if err := tc.local.InsertMessages([]db.Message{{
			SessionID: tc.id, Ordinal: 0,
			Role: "user", Content: "hi",
			ContentLength: 2,
		}}); err != nil {
			t.Fatalf("InsertMessages (%s): %v", tc.mach, err)
		}

		// Push from each machine.
		if _, err := tc.sync.Push(ctx, false); err != nil {
			t.Fatalf("Push (%s): %v", tc.mach, err)
		}
	}

	// Set backfill_pending for both machines.
	for _, m := range []string{"machine-a", "machine-b"} {
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO sync_metadata (key, value)
			 VALUES ($1, 'true')`,
			"backfill_pending:"+m,
		); err != nil {
			t.Fatalf("inserting backfill for %s: %v", m, err)
		}
	}

	// Full push from machine-a only.
	if _, err := syncA.Push(ctx, true); err != nil {
		t.Fatalf("Push (full, a): %v", err)
	}

	// machine-a cleared, machine-b still set.
	if IsBackfillPendingForMachine(ctx, pg, "machine-a") {
		t.Error("machine-a backfill still pending after " +
			"full push")
	}
	if !IsBackfillPendingForMachine(ctx, pg, "machine-b") {
		t.Error("machine-b backfill should still be pending")
	}

	// Global check should be true (machine-b).
	if !IsBackfillPending(ctx, pg) {
		t.Error("IsBackfillPending = false; want true " +
			"(machine-b still pending)")
	}

	// Full push from machine-b.
	if _, err := syncB.Push(ctx, true); err != nil {
		t.Fatalf("Push (full, b): %v", err)
	}

	// Both cleared.
	if IsBackfillPendingForMachine(ctx, pg, "machine-b") {
		t.Error("machine-b backfill still pending after " +
			"full push")
	}
	if IsBackfillPending(ctx, pg) {
		t.Error("IsBackfillPending = true; want false " +
			"(both cleared)")
	}
}

// TestPendingBackfillMachines verifies the PendingBackfillMachines
// function across various states of flags and sessions.
func TestPendingBackfillMachines(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_machines_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// No pending — empty list.
	machines, err := PendingBackfillMachines(ctx, pg)
	if err != nil {
		t.Fatalf("PendingBackfillMachines (empty): %v", err)
	}
	if len(machines) != 0 {
		t.Errorf("want empty list, got %v", machines)
	}

	// Insert sessions with messages for machine-a and machine-b.
	for _, m := range []string{"machine-a", "machine-b"} {
		sessID := "sess-" + m
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO sessions
			 (id, machine, project, agent, created_at,
			  updated_at)
			 VALUES ($1, $2, 'proj', 'claude',
			         now(), now())`,
			sessID, m,
		); err != nil {
			t.Fatalf("inserting session for %s: %v", m, err)
		}
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO messages
			 (session_id, ordinal, role, content, timestamp,
			  content_length, is_system)
			 VALUES ($1, 0, 'user', 'msg', now(), 3, FALSE)`,
			sessID,
		); err != nil {
			t.Fatalf("inserting message for %s: %v", m, err)
		}
		if _, err := pg.ExecContext(ctx,
			`INSERT INTO sync_metadata (key, value)
			 VALUES ($1, 'true')`,
			"backfill_pending:"+m,
		); err != nil {
			t.Fatalf("inserting flag for %s: %v", m, err)
		}
	}

	// Both machines pending.
	machines, err = PendingBackfillMachines(ctx, pg)
	if err != nil {
		t.Fatalf("PendingBackfillMachines (both): %v", err)
	}
	sort.Strings(machines)
	if len(machines) != 2 ||
		machines[0] != "machine-a" ||
		machines[1] != "machine-b" {
		t.Errorf("want [machine-a machine-b], got %v",
			machines)
	}

	// Clear machine-a's flag.
	if err := ClearBackfillPending(
		ctx, pg, "machine-a",
	); err != nil {
		t.Fatalf("ClearBackfillPending (a): %v", err)
	}

	machines, err = PendingBackfillMachines(ctx, pg)
	if err != nil {
		t.Fatalf("PendingBackfillMachines (after clear): %v",
			err)
	}
	if len(machines) != 1 || machines[0] != "machine-b" {
		t.Errorf("want [machine-b], got %v", machines)
	}

	// Delete machine-b's messages and sessions (keep flag).
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM messages WHERE session_id = 'sess-machine-b'`,
	); err != nil {
		t.Fatalf("deleting machine-b messages: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM sessions WHERE machine = 'machine-b'`,
	); err != nil {
		t.Fatalf("deleting machine-b sessions: %v", err)
	}

	// Should return empty (flag exists but no sessions).
	machines, err = PendingBackfillMachines(ctx, pg)
	if err != nil {
		t.Fatalf("PendingBackfillMachines (no sessions): %v",
			err)
	}
	if len(machines) != 0 {
		t.Errorf("want empty list, got %v", machines)
	}

	// Re-create machine-b's session WITHOUT messages.
	// The flag still exists; the session has zero messages.
	// PendingBackfillMachines should still return empty.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, machine, project, agent, created_at,
		  updated_at)
		 VALUES ('sess-machine-b-2', 'machine-b', 'proj',
		         'claude', now(), now())`,
	); err != nil {
		t.Fatalf("re-inserting machine-b session: %v", err)
	}
	machines, err = PendingBackfillMachines(ctx, pg)
	if err != nil {
		t.Fatalf("PendingBackfillMachines (zero-msg): %v",
			err)
	}
	if len(machines) != 0 {
		t.Errorf("want empty (zero-msg session), got %v",
			machines)
	}
}

// TestClearBackfillPendingNonExistent verifies that
// ClearBackfillPending returns an error when no flag exists
// for the given machine.
func TestClearBackfillPendingNonExistent(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_noexist_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	err = ClearBackfillPending(ctx, pg, "no-such-machine")
	if err == nil {
		t.Fatal("ClearBackfillPending returned nil; " +
			"want error")
	}
	if !strings.Contains(
		err.Error(), "no backfill_pending flag found",
	) {
		t.Errorf("error = %v; want message containing "+
			"'no backfill_pending flag found'", err)
	}
}

// TestCheckSchemaCompatBackfillLifecycle verifies that
// CheckSchemaCompat correctly reports errors when backfill
// is pending and succeeds when it is cleared.
func TestCheckSchemaCompatBackfillLifecycle(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_compat_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	// Fresh schema, no backfill — should be compatible.
	if err := CheckSchemaCompat(ctx, pg); err != nil {
		t.Fatalf("CheckSchemaCompat (clean) = %v; want nil",
			err)
	}

	// Insert a session with a message for machine-a and set
	// backfill_pending.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sessions
		 (id, machine, project, agent, created_at, updated_at)
		 VALUES ('compat-sess', 'machine-a', 'proj', 'claude',
		         now(), now())`,
	); err != nil {
		t.Fatalf("inserting session: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO messages
		 (session_id, ordinal, role, content, timestamp,
		  content_length, is_system)
		 VALUES ('compat-sess', 0, 'user', 'msg', now(), 3, FALSE)`,
	); err != nil {
		t.Fatalf("inserting message: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:machine-a', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill flag: %v", err)
	}

	// CheckSchemaCompat should return error mentioning
	// machine-a.
	err = CheckSchemaCompat(ctx, pg)
	if err == nil {
		t.Fatal("CheckSchemaCompat = nil; want error " +
			"about pending backfill")
	}
	if !strings.Contains(err.Error(), "machine-a") {
		t.Errorf("error = %v; want mention of machine-a",
			err)
	}

	// Clear the flag.
	if err := ClearBackfillPending(
		ctx, pg, "machine-a",
	); err != nil {
		t.Fatalf("ClearBackfillPending: %v", err)
	}

	// CheckSchemaCompat should return nil.
	if err := CheckSchemaCompat(ctx, pg); err != nil {
		t.Errorf("CheckSchemaCompat (after clear) = %v; "+
			"want nil", err)
	}

	// Re-insert backfill_pending for machine-a, then remove
	// the message so the session has zero messages. Compat
	// should pass because zero-message sessions have nothing
	// to backfill.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:machine-a', 'true')
		 ON CONFLICT (key) DO UPDATE SET value = 'true'`,
	); err != nil {
		t.Fatalf("re-inserting backfill flag: %v", err)
	}
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM messages
		 WHERE session_id = 'compat-sess'`,
	); err != nil {
		t.Fatalf("deleting messages: %v", err)
	}
	if err := CheckSchemaCompat(ctx, pg); err != nil {
		t.Errorf("CheckSchemaCompat (zero-msg session) "+
			"= %v; want nil", err)
	}
	// Clean up the flag for the next section.
	if _, err := pg.ExecContext(ctx,
		`DELETE FROM sync_metadata
		 WHERE key = 'backfill_pending:machine-a'`,
	); err != nil {
		t.Fatalf("cleaning up backfill flag: %v", err)
	}

	// Downgrade schema_version to 1.
	if _, err := pg.ExecContext(ctx,
		`UPDATE sync_metadata SET value = '1'
		 WHERE key = 'schema_version'`,
	); err != nil {
		t.Fatalf("downgrading schema_version: %v", err)
	}

	// CheckSchemaCompat should return error about version.
	err = CheckSchemaCompat(ctx, pg)
	if err == nil {
		t.Fatal("CheckSchemaCompat = nil; want error " +
			"about schema version")
	}
	wantMsg := fmt.Sprintf("schema version 1 < %d",
		SchemaVersion)
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error = %v; want message containing %q",
			err, wantMsg)
	}
}

// TestPushSchemaDoneFalseAndBackfillPending verifies that
// when both triggers fire (schemaDone=false with PG version < 2,
// AND backfill_pending is set), the push correctly does a single
// full push that satisfies both conditions.
func TestPushSchemaDoneFalseAndBackfillPending(t *testing.T) {
	pgURL := testPGURL(t)

	const schema = "agentsview_backfill_both_test"
	pg, err := Open(pgURL, schema, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	if _, err := pg.Exec(
		`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
	); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := EnsureSchema(ctx, pg, schema); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

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
		machine:    "test-machine",
		schema:     schema,
		schemaDone: true,
	}

	const sessID = "both-trigger-001"
	sess := db.Session{
		ID:           sessID,
		Project:      "test-proj",
		Machine:      "test-machine",
		Agent:        "claude",
		MessageCount: 2,
		CreatedAt:    "2026-01-01T00:00:00Z",
	}
	if err := localDB.UpsertSession(sess); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	msgs := []db.Message{
		{
			SessionID: sessID, Ordinal: 0,
			Role: "user", Content: "hello",
			ContentLength: 5, IsSystem: false,
		},
		{
			SessionID: sessID, Ordinal: 1,
			Role: "assistant", Content: "world",
			ContentLength: 5, IsSystem: false,
		},
	}
	if err := localDB.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// Initial push.
	if _, err := sync.Push(ctx, false); err != nil {
		t.Fatalf("Push (initial): %v", err)
	}

	// Downgrade schema_version to 1.
	if _, err := pg.ExecContext(ctx,
		`UPDATE sync_metadata SET value = '1'
		 WHERE key = 'schema_version'`,
	); err != nil {
		t.Fatalf("downgrading schema_version: %v", err)
	}

	// Set backfill_pending for this machine.
	if _, err := pg.ExecContext(ctx,
		`INSERT INTO sync_metadata (key, value)
		 VALUES ('backfill_pending:test-machine', 'true')`,
	); err != nil {
		t.Fatalf("inserting backfill_pending: %v", err)
	}

	// Update local: mark ordinal 0 as is_system=true.
	msgs[0].IsSystem = true
	if err := localDB.ReplaceSessionMessages(
		sessID, msgs,
	); err != nil {
		t.Fatalf("ReplaceSessionMessages: %v", err)
	}

	// Clear watermark and boundary state.
	if err := localDB.SetSyncState(
		"last_push_at", "",
	); err != nil {
		t.Fatalf("clearing last_push_at: %v", err)
	}
	if err := localDB.SetSyncState(
		lastPushBoundaryStateKey, "",
	); err != nil {
		t.Fatalf("clearing boundary state: %v", err)
	}

	// Create Sync with schemaDone=false — both triggers fire.
	sync2 := &Sync{
		pg:         pg,
		local:      localDB,
		machine:    "test-machine",
		schema:     schema,
		schemaDone: false,
	}

	if _, err := sync2.Push(ctx, false); err != nil {
		t.Fatalf("Push (both triggers): %v", err)
	}

	// Verify PG has updated is_system.
	wantSystem := map[int]bool{0: true}
	checkIsSystem(t, pg, sessID, wantSystem, 2)

	// Verify schema_version=2.
	ver, err := GetSchemaVersion(ctx, pg)
	if err != nil {
		t.Fatalf("GetSchemaVersion: %v", err)
	}
	if ver != SchemaVersion {
		t.Errorf("schema_version = %d; want %d",
			ver, SchemaVersion)
	}

	// Verify backfill_pending cleared.
	var count int
	if err := pg.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_metadata
		 WHERE key = 'backfill_pending:test-machine'`,
	).Scan(&count); err != nil {
		t.Fatalf("querying backfill_pending: %v", err)
	}
	if count != 0 {
		t.Errorf("backfill_pending still present; "+
			"want 0, got %d", count)
	}
}
