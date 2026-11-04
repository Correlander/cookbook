package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const federationResourceLimitsDDL = `
CREATE TABLE federation_resource_limits (
	global_id INTEGER PRIMARY KEY CHECK(global_id=1),
	object_rows INTEGER NOT NULL CHECK(object_rows BETWEEN 1 AND 1000000),
	object_bytes INTEGER NOT NULL CHECK(object_bytes BETWEEN 1 AND 4294967296),
	event_rows INTEGER NOT NULL CHECK(event_rows BETWEEN 1 AND 1000000),
    event_bytes INTEGER NOT NULL CHECK(event_bytes BETWEEN 1 AND 4294967296),
	nonce_rows INTEGER NOT NULL CHECK(nonce_rows BETWEEN 1 AND 1000000),
	nonce_bytes INTEGER NOT NULL CHECK(nonce_bytes BETWEEN 1 AND 1073741824),
    default_object_rows INTEGER NOT NULL CHECK(default_object_rows BETWEEN 1 AND 100000),
    default_object_bytes INTEGER NOT NULL CHECK(default_object_bytes BETWEEN 1 AND 536870912),
    default_event_rows INTEGER NOT NULL CHECK(default_event_rows BETWEEN 1 AND 100000),
    default_event_bytes INTEGER NOT NULL CHECK(default_event_bytes BETWEEN 1 AND 1073741824),
    default_nonce_rows INTEGER NOT NULL CHECK(default_nonce_rows BETWEEN 1 AND 100000),
    default_nonce_bytes INTEGER NOT NULL CHECK(default_nonce_bytes BETWEEN 1 AND 268435456)
);
INSERT INTO federation_resource_limits(global_id,object_rows,object_bytes,event_rows,event_bytes,nonce_rows,nonce_bytes,default_object_rows,default_object_bytes,default_event_rows,default_event_bytes,default_nonce_rows,default_nonce_bytes)
	VALUES(1,10000,67108864,100000,268435456,10000,16777216,1000,16777216,10000,67108864,1000,2097152);
CREATE TABLE federation_origin_resource_limits (
	origin TEXT PRIMARY KEY,
	object_rows INTEGER NOT NULL CHECK(object_rows BETWEEN 1 AND 100000),
	object_bytes INTEGER NOT NULL CHECK(object_bytes BETWEEN 1 AND 536870912),
	event_rows INTEGER NOT NULL CHECK(event_rows BETWEEN 1 AND 100000),
	event_bytes INTEGER NOT NULL CHECK(event_bytes BETWEEN 1 AND 1073741824),
	nonce_rows INTEGER NOT NULL CHECK(nonce_rows BETWEEN 1 AND 100000),
	nonce_bytes INTEGER NOT NULL CHECK(nonce_bytes BETWEEN 1 AND 268435456)
);
CREATE TABLE federation_remote_catalog (
	source_origin TEXT NOT NULL,
	object_id TEXT NOT NULL,
	source_recipe_id TEXT NOT NULL,
	high_water_sequence INTEGER NOT NULL CHECK(high_water_sequence > 0),
	deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
	state_known INTEGER NOT NULL CHECK(state_known IN (0,1)),
	logical_bytes INTEGER NOT NULL CHECK(logical_bytes >= 0),
	PRIMARY KEY(source_origin,object_id),
	UNIQUE(source_origin,source_recipe_id)
);
CREATE INDEX federation_remote_catalog_origin ON federation_remote_catalog(source_origin,object_id);
`

const federationReceivedEventsV11DDL = `CREATE TABLE federation_received_events (
	source_origin TEXT NOT NULL,
	event_id TEXT NOT NULL,
	body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
	object_id TEXT NOT NULL,
	sequence INTEGER NOT NULL CHECK(sequence>0),
	received_at TEXT NOT NULL,
	response_status INTEGER NOT NULL,
	response_body TEXT NOT NULL,
	logical_bytes INTEGER NOT NULL CHECK(logical_bytes>=0),
	PRIMARY KEY(source_origin,event_id)
)`

const federationNonceResourceSchemaDDL = `ALTER TABLE federation_nonces ADD COLUMN source_origin TEXT NOT NULL DEFAULT '';
ALTER TABLE federation_nonces ADD COLUMN logical_bytes INTEGER NOT NULL DEFAULT 72 CHECK(logical_bytes >= 0);
CREATE INDEX federation_nonces_origin_expiry ON federation_nonces(source_origin,expires_at,nonce_hash)`

const federationResourceMigrationBatchSize = 32

func applyFederationResourceLimitsMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, federationResourceLimitsDDL); err != nil {
		return fmt.Errorf("create federation resource schema: %w", err)
	}
	if _, err := exec.ExecContext(ctx, federationNonceResourceSchemaDDL); err != nil {
		return fmt.Errorf("extend nonce resource accounting: %w", err)
	}
	if err := backfillFederationResourceProjections(ctx, exec); err != nil {
		return err
	}
	if err := backfillFederationRemoteCatalog(ctx, exec); err != nil {
		return err
	}
	if err := backfillFederationReceiptCatalog(ctx, exec); err != nil {
		return err
	}
	if err := preserveFederationReceivedEvents(ctx, exec); err != nil {
		return err
	}
	return nil
}

func backfillFederationResourceProjections(ctx context.Context, exec sqliteExecutor) error {
	cursor := ""
	for {
		rows, err := exec.QueryContext(ctx, `SELECT r.object_id
			FROM federation_remote_recipes r LEFT JOIN federation_remote_recipe_search p ON p.object_id=r.object_id
			WHERE r.deleted=0 AND r.object_id>? AND (p.object_id IS NULL OR p.projection_version<?)
			ORDER BY r.object_id LIMIT ?`, cursor, remoteRecipeSearchProjectionVersion, federationResourceMigrationBatchSize)
		if err != nil {
			return fmt.Errorf("read federation projection backfill: %w", err)
		}
		objectIDs := make([]string, 0, federationResourceMigrationBatchSize)
		for rows.Next() {
			var objectID string
			if err := rows.Scan(&objectID); err != nil {
				rows.Close()
				return fmt.Errorf("scan federation projection backfill: %w", err)
			}
			objectIDs = append(objectIDs, objectID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("read federation projection backfill: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close federation projection backfill: %w", err)
		}
		if len(objectIDs) == 0 {
			return nil
		}
		for _, objectID := range objectIDs {
			var item RemoteRecipeReadView
			var body string
			if err := exec.QueryRowContext(ctx, `SELECT object_id,source_origin,source_recipe_id,creator_name,recipe_json,sequence,updated_at FROM federation_remote_recipes WHERE object_id=?`, objectID).Scan(&item.ObjectURI, &item.SourceOrigin, &item.SourceRecipeID, &item.CreatorName, &body, &item.Sequence, &item.UpdatedAt); err != nil {
				return fmt.Errorf("read federation projection source: %w", err)
			}
			if err := writeRemoteRecipeSearchProjectionExec(ctx, exec, item, body); err != nil {
				return fmt.Errorf("write federation projection backfill: %w", err)
			}
		}
		cursor = objectIDs[len(objectIDs)-1]
	}
}

func backfillFederationRemoteCatalog(ctx context.Context, exec migrationExecutor) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes)
		SELECT r.source_origin,r.object_id,r.source_recipe_id,r.sequence,r.deleted,1,
			length(CAST(r.source_origin AS BLOB))+length(CAST(r.object_id AS BLOB))+length(CAST(r.source_recipe_id AS BLOB))+
			length(CAST(r.creator_name AS BLOB))+length(CAST(r.recipe_json AS BLOB))+
			length(CAST(COALESCE(s.title,'') AS BLOB))+length(CAST(COALESCE(s.description,'') AS BLOB))+length(CAST(COALESCE(s.tags_json,'[]') AS BLOB))+
			length(CAST(CAST(r.sequence AS TEXT) AS BLOB))+length(CAST(CAST(r.deleted AS TEXT) AS BLOB))+1
		FROM federation_remote_recipes r LEFT JOIN federation_remote_recipe_search s ON s.object_id=r.object_id`)
	if err != nil {
		return fmt.Errorf("write remote catalog backfill: %w", err)
	}
	return nil
}

func backfillFederationReceiptCatalog(ctx context.Context, exec migrationExecutor) error {
	const marker = "/federation/recipes/"
	var invalidIdentity int
	if err := exec.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM federation_received_events e LEFT JOIN federation_remote_catalog c ON c.source_origin=e.source_origin AND c.object_id=e.object_id
		WHERE c.object_id IS NULL AND (
			substr(e.object_id,1,length(e.source_origin || ?))<>(e.source_origin || ?) OR
			length(substr(e.object_id,length(e.source_origin || ?)+1))=0 OR
			instr(substr(e.object_id,length(e.source_origin || ?)+1),'/')>0))`, marker, marker, marker, marker, marker).Scan(&invalidIdentity); err != nil {
		return fmt.Errorf("validate receipt high-water identities: %w", err)
	}
	if invalidIdentity != 0 {
		return errors.New("receipt high-water backfill found invalid object identity")
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO federation_remote_catalog(source_origin,object_id,source_recipe_id,high_water_sequence,deleted,state_known,logical_bytes)
		SELECT e.source_origin,e.object_id,substr(e.object_id,length(e.source_origin || ?)+1),MAX(e.sequence),0,0,
			length(CAST(e.source_origin AS BLOB))+length(CAST(e.object_id AS BLOB))+
			length(CAST(substr(e.object_id,length(e.source_origin || ?)+1) AS BLOB))+length(CAST('[]' AS BLOB))+
			length(CAST(CAST(MAX(e.sequence) AS TEXT) AS BLOB))+length(CAST('0' AS BLOB))+1
		FROM federation_received_events e LEFT JOIN federation_remote_catalog c ON c.source_origin=e.source_origin AND c.object_id=e.object_id
		WHERE c.object_id IS NULL GROUP BY e.source_origin,e.object_id`, marker, marker)
	if err != nil {
		return fmt.Errorf("write receipt high-water backfill: %w", err)
	}
	return nil
}

func preserveFederationReceivedEvents(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, `CREATE TABLE federation_received_events_v11 (
		source_origin TEXT NOT NULL,event_id TEXT NOT NULL,body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
		object_id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>0),received_at TEXT NOT NULL,
		response_status INTEGER NOT NULL,response_body TEXT NOT NULL,logical_bytes INTEGER NOT NULL CHECK(logical_bytes>=0),
		PRIMARY KEY(source_origin,event_id))`); err != nil {
		return fmt.Errorf("create durable receipt table: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `INSERT INTO federation_received_events_v11(source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body,logical_bytes)
		SELECT source_origin,event_id,body_digest,object_id,sequence,received_at,response_status,response_body,
			length(CAST(source_origin AS BLOB))+length(CAST(event_id AS BLOB))+length(body_digest)+length(CAST(object_id AS BLOB))+
			length(CAST(CAST(sequence AS TEXT) AS BLOB))+length(CAST(received_at AS BLOB))+
			length(CAST(CAST(response_status AS TEXT) AS BLOB))+length(CAST(response_body AS BLOB))
		FROM federation_received_events`); err != nil {
		return fmt.Errorf("write receipt backfill: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `DROP TABLE federation_received_events`); err != nil {
		return fmt.Errorf("replace legacy receipt table: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `ALTER TABLE federation_received_events_v11 RENAME TO federation_received_events`); err != nil {
		return fmt.Errorf("activate durable receipt table: %w", err)
	}
	return nil
}

func remoteCatalogLogicalBytes(origin, object, recipeID, creator, body, title, description, tags string, sequence int64, deleted int) int64 {
	return int64(len(origin) + len(object) + len(recipeID) + len(creator) + len(body) + len(title) + len(description) + len(tags) + len(fmt.Sprint(sequence)) + len(fmt.Sprint(deleted)) + 1)
}

func receivedEventLogicalBytes(origin, id string, digest []byte, object string, sequence int64, received string, status int, response string) int64 {
	return int64(len(origin) + len(id) + len(digest) + len(object) + len(fmt.Sprint(sequence)) + len(received) + len(fmt.Sprint(status)) + len(response))
}

func readFederationResourceLimits(ctx context.Context, exec sqliteExecutor, origin string) (federationResourceLimits, error) {
	limits, err := readFederationDefaultPeerLimits(ctx, exec)
	if err != nil {
		return limits, err
	}
	err = exec.QueryRowContext(ctx, `SELECT object_rows,object_bytes,event_rows,event_bytes,nonce_rows,nonce_bytes FROM federation_origin_resource_limits WHERE origin=?`, origin).Scan(&limits.ObjectRows, &limits.ObjectBytes, &limits.EventRows, &limits.EventBytes, &limits.NonceRows, &limits.NonceBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return limits, nil
	}
	return limits, err
}

func readFederationDefaultPeerLimits(ctx context.Context, exec sqliteExecutor) (federationResourceLimits, error) {
	var limits federationResourceLimits
	err := exec.QueryRowContext(ctx, `SELECT default_object_rows,default_object_bytes,default_event_rows,default_event_bytes,default_nonce_rows,default_nonce_bytes FROM federation_resource_limits WHERE global_id=1`).Scan(&limits.ObjectRows, &limits.ObjectBytes, &limits.EventRows, &limits.EventBytes, &limits.NonceRows, &limits.NonceBytes)
	return limits, err
}

func readFederationGlobalLimits(ctx context.Context, exec sqliteExecutor) (federationResourceLimits, error) {
	var limits federationResourceLimits
	err := exec.QueryRowContext(ctx, `SELECT object_rows,object_bytes,event_rows,event_bytes,nonce_rows,nonce_bytes FROM federation_resource_limits WHERE global_id=1`).Scan(&limits.ObjectRows, &limits.ObjectBytes, &limits.EventRows, &limits.EventBytes, &limits.NonceRows, &limits.NonceBytes)
	return limits, err
}

func readFederationResourceUsage(ctx context.Context, exec sqliteExecutor, origin string, global bool) (federationResourceUsage, error) {
	var usage federationResourceUsage
	if global {
		err := exec.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM federation_remote_catalog),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_remote_catalog),(SELECT COUNT(*) FROM federation_received_events),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_received_events),(SELECT COUNT(*) FROM federation_nonces),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_nonces)`).Scan(&usage.ObjectRows, &usage.ObjectBytes, &usage.EventRows, &usage.EventBytes, &usage.NonceRows, &usage.NonceBytes)
		return usage, err
	}
	err := exec.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM federation_remote_catalog WHERE source_origin=?),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_remote_catalog WHERE source_origin=?),(SELECT COUNT(*) FROM federation_received_events WHERE source_origin=?),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_received_events WHERE source_origin=?),(SELECT COUNT(*) FROM federation_nonces WHERE source_origin=?),(SELECT COALESCE(SUM(logical_bytes),0) FROM federation_nonces WHERE source_origin=?)`, origin, origin, origin, origin, origin, origin).Scan(&usage.ObjectRows, &usage.ObjectBytes, &usage.EventRows, &usage.EventBytes, &usage.NonceRows, &usage.NonceBytes)
	return usage, err
}

func checkFederationResourceLimits(ctx context.Context, exec sqliteExecutor, origin string, nextObjects federationResourceUsage, nextEvents federationResourceUsage) error {
	global, err := readFederationGlobalLimits(ctx, exec)
	if err != nil {
		return err
	}
	peer := defaultFederationResourceLimits()
	if origin != "" {
		peer, err = readFederationResourceLimits(ctx, exec, origin)
		if err != nil {
			return err
		}
	}
	globalUsage, err := readFederationResourceUsage(ctx, exec, "", true)
	if err != nil {
		return err
	}
	peerUsage := federationResourceUsage{}
	if origin != "" {
		peerUsage, err = readFederationResourceUsage(ctx, exec, origin, false)
		if err != nil {
			return err
		}
	}
	if nextObjects.ObjectRows > peer.ObjectRows || nextObjects.ObjectBytes > peer.ObjectBytes || nextEvents.EventRows > peer.EventRows || nextEvents.EventBytes > peer.EventBytes ||
		globalUsage.ObjectRows+nextObjects.ObjectRows > global.ObjectRows || globalUsage.ObjectBytes+nextObjects.ObjectBytes > global.ObjectBytes || globalUsage.EventRows+nextEvents.EventRows > global.EventRows || globalUsage.EventBytes+nextEvents.EventBytes > global.EventBytes ||
		!usageFitsData(peer, addFederationDataUsage(peerUsage, nextObjects, nextEvents)) {
		return errFederationResourceCapacity
	}
	return nil
}

func setFederationResourceLimits(ctx context.Context, db *sql.DB, global, defaults federationResourceLimits, origins []federationResourceOrigin) error {
	if err := validateFederationResourceLimits(global, true); err != nil {
		return err
	}
	if err := validateFederationResourceLimits(defaults, false); err != nil {
		return err
	}
	seenOrigins := map[string]struct{}{}
	for _, item := range origins {
		canonical, err := canonicalFederationOrigin(item.Origin)
		if err != nil || canonical != item.Origin {
			return fmt.Errorf("%w: invalid origin", errFederationResourceInvalid)
		}
		if _, exists := seenOrigins[item.Origin]; exists {
			return fmt.Errorf("%w: duplicate origin", errFederationResourceInvalid)
		}
		seenOrigins[item.Origin] = struct{}{}
		if err := validateFederationResourceLimits(item.Limits, false); err != nil {
			return err
		}
	}
	return withImmediateSQLiteWriteTx(ctx, db, func(tx sqliteExecutor) error {
		usage, err := readFederationResourceUsage(ctx, tx, "", true)
		if err != nil {
			return err
		}
		if !usageFits(global, usage) {
			return errFederationResourceInUse
		}
		for _, item := range origins {
			usage, err := readFederationResourceUsage(ctx, tx, item.Origin, false)
			if err != nil {
				return err
			}
			if !usageFits(item.Limits, usage) {
				return errFederationResourceInUse
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO federation_origin_resource_limits(origin,object_rows,object_bytes,event_rows,event_bytes,nonce_rows,nonce_bytes) VALUES(?,?,?,?,?,?,?) ON CONFLICT(origin) DO UPDATE SET object_rows=excluded.object_rows,object_bytes=excluded.object_bytes,event_rows=excluded.event_rows,event_bytes=excluded.event_bytes,nonce_rows=excluded.nonce_rows,nonce_bytes=excluded.nonce_bytes`, item.Origin, item.Limits.ObjectRows, item.Limits.ObjectBytes, item.Limits.EventRows, item.Limits.EventBytes, item.Limits.NonceRows, item.Limits.NonceBytes); err != nil {
				return err
			}
		}
		inheriting, err := tx.QueryContext(ctx, `SELECT source_origin FROM federation_remote_catalog WHERE source_origin NOT IN (SELECT origin FROM federation_origin_resource_limits) UNION SELECT source_origin FROM federation_received_events WHERE source_origin NOT IN (SELECT origin FROM federation_origin_resource_limits) UNION SELECT source_origin FROM federation_nonces WHERE source_origin<>'' AND source_origin NOT IN (SELECT origin FROM federation_origin_resource_limits)`)
		if err != nil {
			return err
		}
		originsUsingDefaults := []string{}
		for inheriting.Next() {
			var origin string
			if err := inheriting.Scan(&origin); err != nil {
				inheriting.Close()
				return err
			}
			originsUsingDefaults = append(originsUsingDefaults, origin)
		}
		if err := inheriting.Err(); err != nil {
			inheriting.Close()
			return err
		}
		if err := inheriting.Close(); err != nil {
			return err
		}
		for _, origin := range originsUsingDefaults {
			usage, err := readFederationResourceUsage(ctx, tx, origin, false)
			if err != nil {
				return err
			}
			if !usageFits(defaults, usage) {
				return errFederationResourceInUse
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE federation_resource_limits SET object_rows=?,object_bytes=?,event_rows=?,event_bytes=?,nonce_rows=?,nonce_bytes=?,default_object_rows=?,default_object_bytes=?,default_event_rows=?,default_event_bytes=?,default_nonce_rows=?,default_nonce_bytes=? WHERE global_id=1`, global.ObjectRows, global.ObjectBytes, global.EventRows, global.EventBytes, global.NonceRows, global.NonceBytes, defaults.ObjectRows, defaults.ObjectBytes, defaults.EventRows, defaults.EventBytes, defaults.NonceRows, defaults.NonceBytes); err != nil {
			return err
		}
		return nil
	})
}

func usageFits(l federationResourceLimits, u federationResourceUsage) bool {
	return usageFitsData(l, u) && l.NonceRows >= u.NonceRows && l.NonceBytes >= u.NonceBytes
}

func usageFitsData(l federationResourceLimits, u federationResourceUsage) bool {
	return l.ObjectRows >= u.ObjectRows && l.ObjectBytes >= u.ObjectBytes && l.EventRows >= u.EventRows && l.EventBytes >= u.EventBytes
}

func addFederationDataUsage(current, objects, events federationResourceUsage) federationResourceUsage {
	return federationResourceUsage{ObjectRows: current.ObjectRows + objects.ObjectRows, ObjectBytes: current.ObjectBytes + objects.ObjectBytes, EventRows: current.EventRows + events.EventRows, EventBytes: current.EventBytes + events.EventBytes}
}
