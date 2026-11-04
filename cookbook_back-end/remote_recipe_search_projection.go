package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"
)

const (
	remoteRecipeSearchProjectionVersion = 1
	remoteRecipeSearchBackfillBatchSize = 64
	remoteRecipeSearchBackfillRetry     = 250 * time.Millisecond
)

// writeRemoteRecipeSearchProjection conditionally publishes the canonical
// searchable fields only while the captured raw cache source is still current.
// Invalid canonical data receives an empty unavailable projection.
func writeRemoteRecipeSearchProjection(ctx context.Context, tx *sql.Tx, source RemoteRecipeReadView, rawBody string) error {
	view, decodeErr := decodeCachedPublicRemoteRecipe(source.ObjectURI, source.SourceOrigin, source.SourceRecipeID, source, rawBody)
	available := 1
	title, description, tagsJSON := "", "", "[]"
	if decodeErr != nil {
		available = 0
	} else {
		encodedTags, err := json.Marshal(view.Recipe.Tags)
		if err != nil {
			return err
		}
		title, description, tagsJSON = view.Recipe.Title, view.Recipe.Description, string(encodedTags)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO federation_remote_recipe_search(
		object_id,projection_version,available,title,description,tags_json)
		SELECT ?,?,?,?,?,?
		WHERE EXISTS(SELECT 1 FROM federation_remote_recipes
			WHERE object_id=? AND source_origin=? AND source_recipe_id=? AND sequence=?
				AND creator_name=? AND recipe_json=? AND updated_at=? AND deleted=0)
		ON CONFLICT(object_id) DO UPDATE SET
			projection_version=excluded.projection_version,
			available=excluded.available,
			title=excluded.title,
			description=excluded.description,
			tags_json=excluded.tags_json
		WHERE federation_remote_recipe_search.projection_version<=excluded.projection_version`,
		source.ObjectURI, remoteRecipeSearchProjectionVersion, available, title, description, tagsJSON,
		source.ObjectURI, source.SourceOrigin, source.SourceRecipeID, source.Sequence,
		source.CreatorName, rawBody, source.UpdatedAt)
	return err
}

type remoteRecipeSearchSource struct {
	view RemoteRecipeReadView
	body string
}

// startLibraryRemoteSearchBackfill starts a cancellable, restartable worker.
// Platform owns its lifecycle and must cancel then await the returned channel.
func startLibraryRemoteSearchBackfill(ctx context.Context, db *sql.DB) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLibraryRemoteSearchBackfill(ctx, db)
	}()
	return done
}

func runLibraryRemoteSearchBackfill(ctx context.Context, db *sql.DB) {
	for ctx.Err() == nil {
		processed, err := backfillLibraryRemoteSearchPass(ctx, db)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("library remote search backfill batch failed; retrying: %v", err)
			if !waitForLibraryRemoteSearchRetry(ctx) {
				return
			}
			continue
		}
		if !processed {
			return
		}
	}
}

func waitForLibraryRemoteSearchRetry(ctx context.Context) bool {
	timer := time.NewTimer(remoteRecipeSearchBackfillRetry)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func backfillLibraryRemoteSearchPass(ctx context.Context, db *sql.DB) (bool, error) {
	lastObjectID := ""
	processed := false
	for ctx.Err() == nil {
		rows, err := db.QueryContext(ctx, `SELECT r.object_id,r.source_origin,r.source_recipe_id,r.creator_name,
			r.recipe_json,r.sequence,r.updated_at
			FROM federation_remote_recipes r
			LEFT JOIN federation_remote_recipe_search p ON p.object_id=r.object_id
			WHERE r.deleted=0 AND r.object_id>? AND
				(p.object_id IS NULL OR p.projection_version<?)
			ORDER BY r.object_id LIMIT ?`, lastObjectID, remoteRecipeSearchProjectionVersion, remoteRecipeSearchBackfillBatchSize)
		if err != nil {
			return processed, err
		}
		batch := make([]remoteRecipeSearchSource, 0, remoteRecipeSearchBackfillBatchSize)
		for rows.Next() {
			var source remoteRecipeSearchSource
			if err := rows.Scan(&source.view.ObjectURI, &source.view.SourceOrigin, &source.view.SourceRecipeID,
				&source.view.CreatorName, &source.body, &source.view.Sequence, &source.view.UpdatedAt); err != nil {
				rows.Close()
				return processed, err
			}
			batch = append(batch, source)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return processed, err
		}
		if err := rows.Close(); err != nil {
			return processed, err
		}
		if len(batch) == 0 {
			return processed, nil
		}
		processed = true
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return processed, err
		}
		for _, source := range batch {
			if err := ctx.Err(); err != nil {
				tx.Rollback()
				return processed, err
			}
			if err := writeRemoteRecipeSearchProjection(ctx, tx, source.view, source.body); err != nil {
				tx.Rollback()
				return processed, err
			}
		}
		if err := tx.Commit(); err != nil {
			return processed, err
		}
		lastObjectID = batch[len(batch)-1].view.ObjectURI
	}
	return processed, ctx.Err()
}
