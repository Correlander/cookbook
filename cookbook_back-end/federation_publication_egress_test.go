package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func newFederationPublicationEgressDB(t *testing.T, mode RecipeFederationMode) *sql.DB {
	t.Helper()
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('egress-owner','egress@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at)
		VALUES(?, 'egress-owner','Owner','Dish','[]','[]','[]','public','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=1,federation_enabled=1,federation_origin=? WHERE id=1`, testFederationOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at) VALUES(?,'published',?,0,'now')`, testFederationID, mode); err != nil {
		t.Fatal(err)
	}
	if err := registerFederationPeer(db, "https://peer.example.org", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func setFederationPublicationMode(t *testing.T, db *sql.DB, mode RecipeFederationMode) {
	t.Helper()
	if _, err := db.Exec(`UPDATE recipe_publication_policy SET federation_mode=?,updated_at='next' WHERE recipe_id=?`, mode, testFederationID); err != nil {
		t.Fatal(err)
	}
}

func enqueueFederationEgressChange(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func federationOutboxEvents(t *testing.T, db *sql.DB) []federationEvent {
	t.Helper()
	rows, err := db.Query(`SELECT payload FROM federation_outbox ORDER BY sequence,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []federationEvent
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		event, _, err := decodeFederationEvent(payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestFederationPublicationOffBlocksSnapshotAndPeerResync(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationOff)
	enqueueFederationEgressChange(t, db)
	if events := federationOutboxEvents(t, db); len(events) != 0 {
		t.Fatalf("consent-off recipe queued events: %+v", events)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := queueFederationSnapshotsForAll(tx, "https://peer.example.org"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if events := federationOutboxEvents(t, db); len(events) != 0 {
		t.Fatalf("resync bypassed owner consent: %+v", events)
	}
}

func TestFederationOfflineRecipeWritesDoNotRequireAnOriginWithoutPeers(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,created_at) VALUES('offline-owner','offline@example.org','Owner','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipes(id,owner_id,creator_name,title,ingredients_json,steps_json,tags_json,visibility,edit_policy,created_at,updated_at)
		VALUES(?, 'offline-owner','Owner','Private dish','[]','[]','[]','private','owner_only','now','now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recipe_publication_policy(recipe_id,publication_state,federation_mode,index_eligible,updated_at)
		VALUES(?,'unpublished','off',0,'now')`, testFederationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0,federation_enabled=0,federation_origin='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, update := range []bool{false, true} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if update {
			if _, err := tx.Exec(`UPDATE recipes SET title='Updated private dish' WHERE id=?`, testFederationID); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
		if err := enqueueFederationChange(tx, testFederationID); err != nil {
			_ = tx.Rollback()
			t.Fatalf("offline create/update required an origin (update=%t): %v", update, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var objects, queued int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM federation_local_objects),(SELECT COUNT(*) FROM federation_outbox)`).Scan(&objects, &queued); err != nil || objects != 0 || queued != 0 {
		t.Fatalf("offline writes created federation state: objects=%d queued=%d err=%v", objects, queued, err)
	}
}

func TestFederationInvalidOriginFailsWhenPublishedPeerNeedsWithdrawal(t *testing.T) {
	for _, origin := range []string{"", "https:///bad"} {
		t.Run(map[string]string{"": "missing", "https:///bad": "malformed"}[origin], func(t *testing.T) {
			db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
			enqueueFederationEgressChange(t, db)
			if _, err := db.Exec(`DELETE FROM federation_outbox WHERE object_id=?`, testFederationOrigin+"/federation/recipes/"+testFederationID); err != nil {
				t.Fatal(err)
			}
			var peerPublished int
			if err := db.QueryRow(`SELECT published FROM federation_local_peer_objects WHERE peer_origin='https://peer.example.org' AND recipe_id=?`, testFederationID).Scan(&peerPublished); err != nil || peerPublished != 1 {
				t.Fatalf("acknowledged object lost its published marker: published=%d err=%v", peerPublished, err)
			}
			if _, err := db.Exec(`UPDATE instance_settings SET federation_origin=? WHERE id=1`, origin); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE recipe_publication_policy SET federation_mode='off' WHERE recipe_id=?`, testFederationID); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := enqueueFederationChange(tx, testFederationID); err == nil || !strings.Contains(err.Error(), "federation origin is required") {
				_ = tx.Rollback()
				t.Fatalf("withdrawal with invalid origin error=%v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var mode string
			if err := db.QueryRow(`SELECT federation_mode FROM recipe_publication_policy WHERE recipe_id=?`, testFederationID).Scan(&mode); err != nil || mode != string(RecipeFederationFollowers) {
				t.Fatalf("failed withdrawal did not roll back policy: mode=%q err=%v", mode, err)
			}
			events := federationOutboxEvents(t, db)
			if len(events) != 0 {
				t.Fatalf("failed withdrawal recreated a queued event: %+v", events)
			}
		})
	}
}

func TestFederationPublicationRevocationReplacesQueuedSnapshotWithTombstone(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	setFederationPublicationMode(t, db, RecipeFederationOff)
	enqueueFederationEgressChange(t, db)
	events := federationOutboxEvents(t, db)
	if len(events) != 1 || events[0].EventType != federationEventTombstone || events[0].Sequence != 2 {
		t.Fatalf("revocation queue=%+v, want one sequence-2 tombstone", events)
	}
	if events[0].ObjectID != testFederationOrigin+"/federation/recipes/"+testFederationID || events[0].Recipe != nil || events[0].Attribution != nil {
		t.Fatalf("revocation changed identity or included content: %+v", events[0])
	}
	enqueueFederationEgressChange(t, db)
	if events := federationOutboxEvents(t, db); len(events) != 1 || events[0].Sequence != 2 {
		t.Fatalf("repeated off reconciliation was not idempotent: %+v", events)
	}
	setFederationPublicationMode(t, db, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	events = federationOutboxEvents(t, db)
	if len(events) != 2 || events[1].EventType != federationEventSnapshot || events[1].Sequence != 3 || events[1].ObjectID != events[0].ObjectID {
		t.Fatalf("explicit owner re-enable did not queue the next snapshot: %+v", events)
	}
}

func TestFederationPeerResyncTombstonesPriorObjectWhenConsentIsOff(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	setFederationPublicationMode(t, db, RecipeFederationOff)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := queueFederationSnapshotsForAll(tx, "https://peer.example.org"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	events := federationOutboxEvents(t, db)
	if len(events) != 1 || events[0].EventType != federationEventTombstone || events[0].Sequence != 2 {
		t.Fatalf("peer resync bypassed owner consent or missed withdrawal: %+v", events)
	}
}

func TestFederationPublicationEnqueueFailsClosedForIneligibleSources(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{name: "private", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE recipes SET visibility='private' WHERE id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unlisted", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE recipes SET visibility='unlisted' WHERE id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unpublished", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE recipe_publication_policy SET publication_state='unpublished',federation_mode='off' WHERE recipe_id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "withdrawn", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE recipe_publication_policy SET publication_state='withdrawn',federation_mode='off' WHERE recipe_id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "deleted", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE recipes SET deleted_at='deleted' WHERE id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "private instance", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE instance_settings SET public_instance=0 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "global federation disabled", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE instance_settings SET federation_enabled=0 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing policy", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`DELETE FROM recipe_publication_policy WHERE recipe_id=?`, testFederationID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
			test.setup(t, db)
			enqueueFederationEgressChange(t, db)
			if events := federationOutboxEvents(t, db); len(events) != 0 {
				t.Fatalf("ineligible source queued recipe content: %+v", events)
			}
		})
	}
}

func TestFederationDeliveryReconcilesStaleQueuedContentBeforeClaim(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	setFederationPublicationMode(t, db, RecipeFederationOff)
	if _, err := db.Exec(`UPDATE federation_outbox SET attempts=12,next_attempt=?`, time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	claim, err := claimFederationDelivery(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("expected the reconciled tombstone to be claimable")
	}
	event, _, err := decodeFederationEvent(claim.payload)
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != federationEventTombstone || event.Sequence != 2 || event.Recipe != nil {
		t.Fatalf("claim released stale content instead of tombstone: %+v", event)
	}
	if _, err := db.Exec(`UPDATE federation_outbox SET lease_until=NULL WHERE id=?`, claim.id); err != nil {
		t.Fatal(err)
	}
	var staleContent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE json_extract(CAST(payload AS TEXT),'$.event_type') IN (?,?)`, federationEventSnapshot, federationEventUpdate).Scan(&staleContent); err != nil {
		t.Fatal(err)
	}
	if staleContent != 0 {
		t.Fatalf("stale content remains queued: %d", staleContent)
	}
}

func TestFederationWithdrawalWaitsBehindAnActiveSnapshotLease(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	if _, err := db.Exec(`UPDATE federation_outbox SET lease_until=?`, time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	setFederationPublicationMode(t, db, RecipeFederationOff)
	enqueueFederationEgressChange(t, db)
	events := federationOutboxEvents(t, db)
	if len(events) != 2 || events[0].EventType != federationEventSnapshot || events[0].Sequence != 1 || events[1].EventType != federationEventTombstone || events[1].Sequence != 2 || events[1].Recipe != nil {
		t.Fatalf("active lease was not retained before the ordered tombstone: %+v", events)
	}
	claim, err := claimFederationDelivery(db, time.Now())
	if err != nil || claim != nil {
		t.Fatalf("tombstone overtook active snapshot lease: claim=%+v err=%v", claim, err)
	}
	if _, err := db.Exec(`UPDATE federation_outbox SET lease_until=NULL WHERE sequence=1`); err != nil {
		t.Fatal(err)
	}
	claim, err = claimFederationDelivery(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("expected the tombstone to become claimable after the old lease cleared")
	}
	event, _, err := decodeFederationEvent(claim.payload)
	if err != nil || event.EventType != federationEventTombstone || event.Sequence != 2 {
		t.Fatalf("stale snapshot released instead of higher-sequence tombstone: event=%+v err=%v", event, err)
	}
	var staleContent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM federation_outbox WHERE json_extract(CAST(payload AS TEXT),'$.event_type') IN (?,?)`, federationEventSnapshot, federationEventUpdate).Scan(&staleContent); err != nil || staleContent != 0 {
		t.Fatalf("stale content remains queued after lease expiry: count=%d err=%v", staleContent, err)
	}
}

func TestFederationTombstonesDoNotBypassGlobalOrPeerDisable(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{name: "global federation disabled", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE instance_settings SET federation_enabled=0 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "peer disabled", setup: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`UPDATE federation_peers SET enabled=0 WHERE origin='https://peer.example.org'`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
			enqueueFederationEgressChange(t, db)
			setFederationPublicationMode(t, db, RecipeFederationOff)
			enqueueFederationEgressChange(t, db)
			if events := federationOutboxEvents(t, db); len(events) != 1 || events[0].EventType != federationEventTombstone {
				t.Fatalf("expected queued tombstone before disable: %+v", events)
			}
			test.setup(t, db)
			claim, err := claimFederationDelivery(db, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if claim != nil {
				t.Fatalf("disabled federation path released tombstone: %+v", claim)
			}
			if count := len(federationOutboxEvents(t, db)); count != 1 {
				t.Fatalf("disabled federation path discarded queued tombstone: count=%d", count)
			}
		})
	}
}

func TestFederationPeerDisableAloneDoesNotRevokeOwnerConsent(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	if _, err := db.Exec(`UPDATE federation_peers SET enabled=0 WHERE origin='https://peer.example.org'`); err != nil {
		t.Fatal(err)
	}
	enqueueFederationEgressChange(t, db)
	events := federationOutboxEvents(t, db)
	if len(events) != 1 || events[0].EventType != federationEventSnapshot {
		t.Fatalf("disabled peer changed owner consent or discarded its queued event: %+v", events)
	}
	claim, err := claimFederationDelivery(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatalf("disabled peer released an outbound request: %+v", claim)
	}
}

func TestFederationConsentRevocationRollsBackWhenTombstoneInsertFails(t *testing.T) {
	db := newFederationPublicationEgressDB(t, RecipeFederationFollowers)
	enqueueFederationEgressChange(t, db)
	if _, err := db.Exec(`CREATE TRIGGER reject_federation_tombstone BEFORE INSERT ON federation_outbox
		WHEN json_extract(CAST(NEW.payload AS TEXT),'$.event_type')='recipe.tombstone'
		BEGIN SELECT RAISE(ABORT,'injected outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE recipe_publication_policy SET federation_mode='off' WHERE recipe_id=?`, testFederationID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := enqueueFederationChange(tx, testFederationID); err == nil {
		_ = tx.Rollback()
		t.Fatal("expected the injected tombstone insert failure")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`SELECT federation_mode FROM recipe_publication_policy WHERE recipe_id=?`, testFederationID).Scan(&mode); err != nil || mode != string(RecipeFederationFollowers) {
		t.Fatalf("failed egress transaction changed owner consent: mode=%q err=%v", mode, err)
	}
	events := federationOutboxEvents(t, db)
	if len(events) != 1 || events[0].EventType != federationEventSnapshot || events[0].Sequence != 1 {
		t.Fatalf("failed egress transaction changed prior outbox state: %+v", events)
	}
}
