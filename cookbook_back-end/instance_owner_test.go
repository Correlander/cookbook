package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ownerTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedOwnerTestData(t, db)
	return db
}

func seedOwnerTestData(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
('owner-a','owner-a@example.test','Owner A',1,'created-a'),
('owner-b','owner-b@example.test','Owner B',0,'created-b'),
('admin-c','admin-c@example.test','Admin C',1,'created-c');
INSERT INTO recipes(id,owner_id,creator_name,title,description,ingredients_json,steps_json,visibility,edit_policy,version,created_at,updated_at)
VALUES('recipe-a','owner-a','Historic Creator','Keep this recipe','','[]','[]','private','owner_only',3,'recipe-created','recipe-updated');
INSERT INTO recipe_revisions(id,recipe_id,version,editor_id,title,description,ingredients_json,steps_json,visibility,created_at)
VALUES('revision-a','recipe-a',3,'owner-a','Keep this recipe','','[]','[]','private','revision-created');
INSERT INTO cookbooks(id,owner_id,title,description,visibility,created_at,updated_at)
VALUES('book-a','owner-a','Keep this cookbook','','private','book-created','book-updated');
INSERT INTO cookbook_memberships(cookbook_id,user_id,role,created_at)
VALUES('book-a','owner-b','viewer','membership-created');`)
	if err != nil {
		t.Fatal(err)
	}
}

func applyOwnerTestDDL(t *testing.T, db *sql.DB) {
	t.Helper()
	for i := 0; i < 2; i++ { // The actual migration body is replay-safe.
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyInstanceOwnerDDL(context.Background(), tx); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertOwnerUserAndContentPreserved(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT id,is_admin FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantAdmins := map[string]int{"admin-c": 1, "owner-a": 1, "owner-b": 0}
	seen := 0
	for rows.Next() {
		var id string
		var isAdmin int
		if err := rows.Scan(&id, &isAdmin); err != nil {
			t.Fatal(err)
		}
		if want, exists := wantAdmins[id]; !exists || want != isAdmin {
			t.Fatalf("user/admin state changed: %s is_admin=%d", id, isAdmin)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(wantAdmins) {
		t.Fatalf("user count=%d, want %d", seen, len(wantAdmins))
	}
	var recipeOwner, recipeTitle, creator, revisionTitle, bookOwner, memberRole string
	err = db.QueryRow(`SELECT r.owner_id,r.title,r.creator_name,rv.title,c.owner_id,m.role
		FROM recipes r JOIN recipe_revisions rv ON rv.recipe_id=r.id
		JOIN cookbooks c ON c.id='book-a'
		JOIN cookbook_memberships m ON m.cookbook_id=c.id AND m.user_id='owner-b'
		WHERE r.id='recipe-a' AND rv.id='revision-a'`).Scan(&recipeOwner, &recipeTitle, &creator, &revisionTitle, &bookOwner, &memberRole)
	if err != nil {
		t.Fatal(err)
	}
	if recipeOwner != "owner-a" || recipeTitle != "Keep this recipe" || creator != "Historic Creator" || revisionTitle != recipeTitle || bookOwner != "owner-a" || memberRole != "viewer" {
		t.Fatalf("content changed: recipe=%q/%q/%q revision=%q cookbook=%q membership=%q", recipeOwner, recipeTitle, creator, revisionTitle, bookOwner, memberRole)
	}
}

func TestApplyInstanceOwnerDDLIsIdempotentAndSeedsUnclaimedAuthority(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	applyOwnerTestDDL(t, db)
	var owner sql.NullString
	var generation int64
	if err := db.QueryRow(`SELECT owner_user_id,generation FROM instance_authority WHERE singleton_id=1`).Scan(&owner, &generation); err != nil {
		t.Fatal(err)
	}
	if owner.Valid || generation != 0 {
		t.Fatalf("authority owner=%v generation=%d, want unclaimed generation 0", owner, generation)
	}
	if !migrationObjectExists(t, db, "index", "one_pending_owner_transfer") {
		t.Fatal("partial unique index for pending transfers was not installed")
	}
	assertOwnerUserAndContentPreserved(t, db)
}

func TestOwnerClaimIsExplicitAuditedAndDoesNotGrantAdmin(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "missing", now); !errors.Is(err, ErrOwnerAccountNotFound) {
		t.Fatalf("unknown account claim error=%v", err)
	}
	before, err := readInstanceAuthority(ctx, db)
	if err != nil || before.Claimed() || before.Generation != 0 {
		t.Fatalf("unknown claim changed authority: %+v err=%v", before, err)
	}
	claimed, err := claimInstanceOwnerAt(ctx, db, "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.OwnerUserID != "owner-b" || claimed.Generation != 1 || !claimed.ClaimedAt.Equal(now) {
		t.Fatalf("claim result=%+v", claimed)
	}
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now.Add(time.Minute)); !errors.Is(err, ErrInstanceOwnerAlreadySet) {
		t.Fatalf("duplicate claim error=%v", err)
	}
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, "owner-b")
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.OwnerClaimed || !capabilities.IsInstanceOwner || capabilities.IsAdmin || capabilities.Generation != 1 {
		t.Fatalf("non-admin owner capability=%+v", capabilities)
	}
	assertOwnerUserAndContentPreserved(t, db)
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_owner.claim' AND actor_id='owner-b'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("claim audit count=%d err=%v", audits, err)
	}
}

func TestOwnerClaimRollsBackWhenAuditCannotBeRecorded(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	_, err := db.Exec(`CREATE TRIGGER reject_owner_claim_audit BEFORE INSERT ON audit_events
		WHEN NEW.action='instance_owner.claim' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimInstanceOwnerAt(context.Background(), db, "owner-a", time.Now()); err == nil {
		t.Fatal("claim succeeded without its required audit event")
	}
	authority, err := readInstanceAuthority(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if authority.Claimed() || authority.Generation != 0 {
		t.Fatalf("failed audited claim changed authority: %+v", authority)
	}
}

func TestOwnerTransferAuthorizationAcceptanceAndCancellation(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := createInstanceOwnerTransferAt(ctx, db, "admin-c", "owner-b", now); !errors.Is(err, ErrInstanceOwnerUnauthorized) {
		t.Fatalf("non-owner transfer error=%v", err)
	}
	if _, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "missing", now); !errors.Is(err, ErrOwnerAccountNotFound) {
		t.Fatalf("unknown target error=%v", err)
	}
	if _, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-a", now); !errors.Is(err, ErrOwnerTransferSelf) {
		t.Fatalf("self transfer error=%v", err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if transfer.State != OwnerTransferPending || !transfer.ExpiresAt.Equal(now.Add(instanceOwnerTransferLifetime)) {
		t.Fatalf("created transfer=%+v", transfer)
	}
	if _, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "admin-c", now.Add(time.Minute)); !errors.Is(err, ErrOwnerTransferPending) {
		t.Fatalf("second pending transfer error=%v", err)
	}
	if err := cancelInstanceOwnerTransferAt(ctx, db, "admin-c", transfer.ID, now.Add(time.Minute)); !errors.Is(err, ErrInstanceOwnerUnauthorized) {
		t.Fatalf("non-owner cancel error=%v", err)
	}
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "admin-c", transfer.ID, now.Add(time.Minute)); !errors.Is(err, ErrInstanceOwnerUnauthorized) {
		t.Fatalf("wrong acceptor error=%v", err)
	}
	if err := cancelInstanceOwnerTransferAt(ctx, db, "owner-a", transfer.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	cancelled, err := readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || cancelled.State != OwnerTransferCancelled || cancelled.CompletedAt == nil {
		t.Fatalf("cancelled transfer=%+v err=%v", cancelled, err)
	}
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(2*time.Minute)); !errors.Is(err, ErrOwnerTransferNotPending) {
		t.Fatalf("accept cancelled transfer error=%v", err)
	}
	assertOwnerUserAndContentPreserved(t, db)
}

func TestOwnerTransferAcceptAdvancesGenerationOnce(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.OwnerUserID != "owner-b" || accepted.Generation != 2 || !accepted.ClaimedAt.Equal(now) {
		t.Fatalf("accepted authority=%+v", accepted)
	}
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(2*time.Hour)); !errors.Is(err, ErrOwnerTransferNotPending) {
		t.Fatalf("second accept error=%v", err)
	}
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, "owner-b")
	if err != nil || !capabilities.IsInstanceOwner || capabilities.IsAdmin || !capabilities.OwnerClaimed || capabilities.Generation != 2 {
		t.Fatalf("transferred non-admin owner=%+v err=%v", capabilities, err)
	}
	transfer, err = readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || transfer.State != OwnerTransferAccepted || transfer.CompletedAt == nil {
		t.Fatalf("accepted transfer=%+v err=%v", transfer, err)
	}
	assertOwnerUserAndContentPreserved(t, db)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type='instance_owner_transfer' AND resource_id=?`, transfer.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("transfer create/accept audit count=%d err=%v", count, err)
	}
}

func TestOwnerTransferAcceptanceRollsBackIfAuditFails(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER reject_owner_accept_audit BEFORE INSERT ON audit_events
		WHEN NEW.action='instance_owner.transfer.accept' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(time.Minute)); err == nil {
		t.Fatal("transfer succeeded without its required audit event")
	}
	authority, err := readInstanceAuthority(ctx, db)
	if err != nil || authority.OwnerUserID != "owner-a" || authority.Generation != 1 {
		t.Fatalf("failed audited accept changed authority=%+v err=%v", authority, err)
	}
	current, err := readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || current.State != OwnerTransferPending || current.CompletedAt != nil {
		t.Fatalf("failed audited accept changed transfer=%+v err=%v", current, err)
	}
}

func TestExpiredOwnerTransferIsFinalizedAndCannotBeAccepted(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	later := transfer.ExpiresAt.Add(time.Nanosecond)
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, later); !errors.Is(err, ErrOwnerTransferExpired) {
		t.Fatalf("expired accept error=%v", err)
	}
	transfer, err = readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || transfer.State != OwnerTransferExpired || transfer.CompletedAt == nil {
		t.Fatalf("expired transfer=%+v err=%v", transfer, err)
	}
	authority, err := readInstanceAuthority(ctx, db)
	if err != nil || authority.OwnerUserID != "owner-a" || authority.Generation != 1 {
		t.Fatalf("expired acceptance changed authority=%+v err=%v", authority, err)
	}
	if _, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", later); err != nil {
		t.Fatalf("new request after expiry failed: %v", err)
	}
}

func TestOwnerTransferExpirySweepIsIdempotent(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := time.Parse(time.RFC3339Nano, "2026-11-03T12:00:00.1Z")
	if err != nil {
		t.Fatal(err)
	}
	later, err := time.Parse(time.RFC3339Nano, "2026-11-03T12:00:00.1001Z")
	if err != nil {
		t.Fatal(err)
	}
	if !(formatOwnerTime(earlier) < formatOwnerTime(later)) {
		t.Fatalf("fixed-width time order is wrong: %q >= %q", formatOwnerTime(earlier), formatOwnerTime(later))
	}
	if _, err := db.Exec(`UPDATE instance_owner_transfers SET expires_at=? WHERE id=?`, "2026-11-03T12:00:00.1001Z", transfer.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := expireInstanceOwnerTransfersAt(ctx, db, earlier)
	if err != nil || expired != 0 {
		t.Fatalf("mixed-fraction early expiry count=%d err=%v, want 0", expired, err)
	}
	expired, err = expireInstanceOwnerTransfersAt(ctx, db, later)
	if err != nil || expired != 1 {
		t.Fatalf("expiry sweep count=%d err=%v", expired, err)
	}
	expired, err = expireInstanceOwnerTransfersAt(ctx, db, later.Add(time.Hour))
	if err != nil || expired != 0 {
		t.Fatalf("repeat expiry count=%d err=%v", expired, err)
	}
}

func TestOwnerTransferAcceptSamplesTimeAfterWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-lock-expiry.db")
	db := ownerTestDB(t, path)
	blocker, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Second)
	if _, err := db.Exec(`UPDATE instance_owner_transfers SET expires_at=? WHERE id=?`, formatOwnerTime(deadline), transfer.ID); err != nil {
		t.Fatal(err)
	}
	lock, err := blocker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer lock.ExecContext(context.Background(), `ROLLBACK`)

	var released atomic.Bool
	clockSampled := make(chan struct{}, 1)
	clock := func() time.Time {
		clockSampled <- struct{}{}
		if released.Load() {
			return deadline.Add(time.Second)
		}
		return now
	}
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := acceptInstanceOwnerTransferWithClock(ctx, db, "owner-b", transfer.ID, clock)
		result <- err
	}()
	<-started
	select {
	case <-clockSampled:
		t.Fatal("acceptance sampled the decision time before acquiring the writer lock")
	case <-time.After(50 * time.Millisecond):
	}
	released.Store(true)
	if _, err := lock.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrOwnerTransferExpired) {
		t.Fatalf("acceptance after lock wait error=%v, want expired", err)
	}
	current, err := readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || current.State != OwnerTransferExpired {
		t.Fatalf("lock-wait expiry state=%+v err=%v", current, err)
	}
}

func TestStaleOwnerTransferCannotChangeAuthority(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, db, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, db, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_authority SET owner_user_id='admin-c',generation=generation+1 WHERE singleton_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(time.Hour)); !errors.Is(err, ErrOwnerTransferStale) {
		t.Fatalf("stale accept error=%v", err)
	}
	authority, err := readInstanceAuthority(ctx, db)
	if err != nil || authority.OwnerUserID != "admin-c" || authority.Generation != 2 {
		t.Fatalf("stale accept changed authority=%+v err=%v", authority, err)
	}
	unchanged, err := readInstanceOwnerTransfer(ctx, db, transfer.ID)
	if err != nil || unchanged.State != OwnerTransferPending {
		t.Fatalf("stale request state=%+v err=%v", unchanged, err)
	}
}

func TestConcurrentOwnerTransferAcceptanceChangesAuthorityOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-concurrent.db")
	first := ownerTestDB(t, path)
	second, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, err := claimInstanceOwnerAt(ctx, first, "owner-a", now); err != nil {
		t.Fatal(err)
	}
	transfer, err := createInstanceOwnerTransferAt(ctx, first, "owner-a", "owner-b", now)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, db := range []*sql.DB{first, second} {
		wait.Add(1)
		go func(db *sql.DB) {
			defer wait.Done()
			<-start
			_, err := acceptInstanceOwnerTransferAt(ctx, db, "owner-b", transfer.ID, now.Add(time.Hour))
			errs <- err
		}(db)
	}
	close(start)
	wait.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrOwnerTransferNotPending) {
			t.Fatalf("concurrent accept error=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent acceptance successes=%d, want exactly 1", successes)
	}
	authority, err := readInstanceAuthority(ctx, first)
	if err != nil || authority.OwnerUserID != "owner-b" || authority.Generation != 2 {
		t.Fatalf("concurrent accept authority=%+v err=%v", authority, err)
	}
}

func TestConcurrentOwnerClaimsSelectExactlyOneAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-claim-race.db")
	first := ownerTestDB(t, path)
	second, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []struct {
		db        *sql.DB
		accountID string
	}{{first, "owner-a"}, {second, "owner-b"}} {
		wait.Add(1)
		go func(db *sql.DB, accountID string) {
			defer wait.Done()
			<-start
			_, err := claimInstanceOwnerAt(context.Background(), db, accountID, now)
			errs <- err
		}(candidate.db, candidate.accountID)
	}
	close(start)
	wait.Wait()
	close(errs)
	successes, rejected := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInstanceOwnerAlreadySet) {
			rejected++
		} else {
			t.Fatalf("concurrent claim error=%v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("concurrent claim successes=%d rejected=%d, want 1 each", successes, rejected)
	}
	authority, err := readInstanceAuthority(context.Background(), first)
	if err != nil || !authority.Claimed() || authority.Generation != 1 {
		t.Fatalf("concurrent claim authority=%+v err=%v", authority, err)
	}
	var count int
	if err := first.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='instance_owner.claim'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent claim audit count=%d err=%v", count, err)
	}
}

func TestOwnerCapabilitiesAreSeparateFromAdminAndRejectUnknownAccount(t *testing.T) {
	db := ownerTestDB(t, ":memory:")
	ctx := context.Background()
	if _, err := InstanceOwnerCapabilitiesFor(ctx, db, "missing"); !errors.Is(err, ErrOwnerAccountNotFound) {
		t.Fatalf("unknown capability account error=%v", err)
	}
	capabilities, err := InstanceOwnerCapabilitiesFor(ctx, db, "owner-a")
	if err != nil || capabilities.IsInstanceOwner || capabilities.OwnerClaimed || capabilities.Generation != 0 || !capabilities.IsAdmin {
		t.Fatalf("unclaimed admin capabilities=%+v err=%v", capabilities, err)
	}
	if err := cancelInstanceOwnerTransferAt(ctx, db, "owner-a", "missing-transfer", time.Now()); !errors.Is(err, ErrOwnerTransferNotFound) {
		t.Fatalf("missing transfer cancel error=%v", err)
	}
	assertOwnerUserAndContentPreserved(t, db)
}
