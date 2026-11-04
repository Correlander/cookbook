package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func setPrivateSnapshotHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "Cookie")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (h apiHandler) saveRemoteRecipeBookmark(w http.ResponseWriter, r *http.Request, actor ContentActor, input SaveRecipeBookmarkInput) {
	setPrivateSnapshotHeaders(w)
	if input.SaveMode != "" && input.SaveMode != "reference" && input.SaveMode != "conditional" && input.SaveMode != "snapshot" {
		writeError(w, http.StatusBadRequest, "invalid_save_mode")
		return
	}
	mode := input.SaveMode
	if mode == "" {
		mode = "reference"
	}
	var response RecipeBookmarkView
	status := http.StatusOK
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		id, createdAt, found, err := findRecipeBookmark(r.Context(), tx, actor.AccountID, "remote", "", input.Target)
		if err != nil {
			return err
		}
		if found {
			var pinState, versionID, moderatedAt, reason, outcome string
			err := tx.QueryRowContext(r.Context(), `SELECT state,COALESCE(version_id,''),COALESCE(moderated_at,''),COALESCE(moderation_reason,''),COALESCE(moderation_outcome,'') FROM library_remote_snapshot_pins WHERE bookmark_id=?`, id).
				Scan(&pinState, &versionID, &moderatedAt, &reason, &outcome)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			switch pinState {
			case "active":
				item, err := readRemoteSnapshotForBookmark(r.Context(), tx, actor.AccountID, id)
				if err != nil {
					return err
				}
				response = remoteSnapshotBookmarkView(id, createdAt, item, pinState, moderatedAt, reason, outcome)
				return nil
			case "restricted", "removed":
				response = blockedRemoteSnapshotBookmarkView(id, createdAt, input.Target.RemoteObjectURI, pinState, moderatedAt, reason, outcome)
				if mode == "snapshot" {
					if pinState == "restricted" {
						return errRemoteSnapshotRestricted
					}
					return errRemoteSnapshotRemoved
				}
				return nil
			case "":
				if mode == "snapshot" {
					item, err := captureRemoteSnapshot(r.Context(), tx, actor.AccountID, id, createdAt)
					if err != nil {
						return err
					}
					response = remoteSnapshotBookmarkView(id, createdAt, item, "active", "", "", "")
					return nil
				}
				cached, cacheErr := readCachedPublicRemoteRecipe(r.Context(), tx, actor, input.Target.RemoteObjectURI)
				response = referenceRemoteBookmarkView(id, createdAt, input.Target.RemoteObjectURI, cached, cacheErr)
				return nil
			default:
				return errRemoteSnapshotEvidence
			}
		}

		cached, cacheErr := readCachedPublicRemoteRecipe(r.Context(), tx, actor, input.Target.RemoteObjectURI)
		if errors.Is(cacheErr, errRemoteRecipeNotFound) {
			return errRemoteSnapshotNotFound
		}
		if cacheErr != nil {
			return cacheErr
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM recipe_bookmarks WHERE user_id=?`, actor.AccountID).Scan(&count); err != nil {
			return err
		}
		if count >= maxRecipeBookmarksPerAccount {
			return errors.New("bookmark_limit_reached")
		}
		id = newID()
		createdAt = timestamp()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO recipe_bookmarks(id,user_id,target_kind,remote_object_uri,created_at) VALUES(?,?,'remote',?,?)`, id, actor.AccountID, input.Target.RemoteObjectURI, createdAt); err != nil {
			if strings.Contains(err.Error(), "recipe bookmark limit reached") {
				return errors.New("bookmark_limit_reached")
			}
			return err
		}
		if mode == "snapshot" || mode == "conditional" {
			item, captureErr := captureRemoteSnapshot(r.Context(), tx, actor.AccountID, id, createdAt)
			if captureErr == nil {
				response = remoteSnapshotBookmarkView(id, createdAt, item, "active", "", "", "")
				status = http.StatusCreated
				return nil
			}
			if !(mode == "conditional" && errors.Is(captureErr, errRemoteSnapshotNotEligible)) {
				return captureErr
			}
		}
		response = referenceRemoteBookmarkView(id, createdAt, input.Target.RemoteObjectURI, cached, nil)
		status = http.StatusCreated
		return nil
	})
	if err != nil {
		writeRemoteSnapshotError(w, err)
		return
	}
	writeJSON(w, status, response)
}

func referenceRemoteBookmarkView(id, createdAt, objectURI string, cached RemoteRecipeReadView, cacheErr error) RecipeBookmarkView {
	view := RecipeBookmarkView{ID: id, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: objectURI},
		Availability: "unavailable", SaveState: "reference", CreatedAt: createdAt, SourceState: "unknown_legacy"}
	if cacheErr == nil {
		view.Availability = "available"
		view.SourceState = "published"
		view.RemoteRecipe = &cached
	}
	return view
}

func remoteSnapshotBookmarkView(id, createdAt string, item remoteSnapshotVersion, state, moderatedAt, reason, outcome string) RecipeBookmarkView {
	content, _ := parseRemoteSnapshotPayload(item.Payload)
	remote := PrivateSnapshotRecipeReadView{ObjectURI: item.ObjectURI, SourceOrigin: item.SourceOrigin, SourceRecipeID: item.SourceRecipeID,
		CreatorName: item.CreatorName, Recipe: content, Sequence: remoteSnapshotSequenceValue(item.Sequence), UpdatedAt: item.ReceivedAt}
	return RecipeBookmarkView{ID: id, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: item.ObjectURI},
		Availability: "available", SnapshotRecipe: &remote, SaveState: state, VersionID: item.VersionID,
		SourceState: item.SourceState, ProvenanceState: item.Provenance, PublisherRetentionClaim: item.GrantPurpose,
		ModeratedAt: moderatedAt, ModerationReason: reason, ModerationOutcome: outcome, CreatedAt: createdAt,
		Saved: true, SnapshotAvailable: true}
}

func blockedRemoteSnapshotBookmarkView(id, createdAt, objectURI, state, moderatedAt, reason, outcome string) RecipeBookmarkView {
	return RecipeBookmarkView{ID: id, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: objectURI},
		Availability: "unavailable", SaveState: state, ModeratedAt: moderatedAt,
		ModerationReason: reason, ModerationOutcome: outcome, CreatedAt: createdAt, Saved: true}
}

func writeRemoteSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRemoteSnapshotNotFound), errors.Is(err, errRemoteSnapshotRestricted), errors.Is(err, errRemoteSnapshotRemoved):
		code := "not_found"
		if errors.Is(err, errRemoteSnapshotRestricted) {
			code = "snapshot_restricted"
		} else if errors.Is(err, errRemoteSnapshotRemoved) {
			code = "snapshot_removed"
		}
		writeError(w, http.StatusNotFound, code)
	case errors.Is(err, errRemoteSnapshotNotEligible):
		writeError(w, http.StatusConflict, "snapshot_not_eligible")
	case errors.Is(err, errRemoteSnapshotEvidence):
		writeError(w, http.StatusConflict, "snapshot_evidence_invalid")
	case errors.Is(err, errRemoteSnapshotTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "snapshot_capture_too_large")
	case errors.Is(err, errRemoteSnapshotAccountCap):
		writeError(w, http.StatusConflict, "snapshot_limit_reached")
	case errors.Is(err, errRemoteSnapshotStoreCap):
		writeError(w, http.StatusConflict, "snapshot_storage_limit_reached")
	case err.Error() == "bookmark_limit_reached":
		writeError(w, http.StatusConflict, "bookmark_limit_reached")
	default:
		writeError(w, http.StatusInternalServerError, "storage_error")
	}
}

func (h apiHandler) remoteRecipeBookmarkStatus(w http.ResponseWriter, r *http.Request, userID, objectURI string) {
	setPrivateSnapshotHeaders(w)
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if _, _, err := parseFederationRecipeObjectURI(objectURI); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bookmark_target")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	var id, createdAt string
	err = tx.QueryRowContext(r.Context(), `SELECT id,created_at FROM recipe_bookmarks WHERE user_id=? AND target_kind='remote' AND remote_object_uri=?`, userID, objectURI).Scan(&id, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, cacheErr := readCachedPublicRemoteRecipe(r.Context(), tx, ContentActor{AccountID: userID}, objectURI); errors.Is(cacheErr, errRemoteRecipeNotFound) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		} else if cacheErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	states, err := readRemoteSnapshotBookmarkStates(r.Context(), tx, userID, []string{id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	state := states[id]
	response := RecipeBookmarkView{ID: id, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: objectURI},
		Availability: "unavailable", SaveState: "reference", SourceState: state.SourceState, CreatedAt: createdAt,
		ModeratedAt: state.ModeratedAt, ModerationReason: state.Reason, ModerationOutcome: state.Outcome}
	canCapture := false
	if state.State == "active" {
		item, readErr := readRemoteSnapshotForBookmark(r.Context(), tx, userID, id)
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		response = remoteSnapshotBookmarkView(id, createdAt, item, "snapshot", state.ModeratedAt, state.Reason, state.Outcome)
	} else if state.State == "restricted" || state.State == "removed" {
		response = blockedRemoteSnapshotBookmarkView(id, createdAt, objectURI, state.State, state.ModeratedAt, state.Reason, state.Outcome)
		response.SourceState = state.SourceState
	} else {
		cached, cacheErr := readCachedPublicRemoteRecipeResults(r.Context(), tx, ContentActor{AccountID: userID}, []string{objectURI})
		if cacheErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if !cached[objectURI].Available {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		response.Availability = "available"
		response.SourceState = state.SourceState
		remote := cached[objectURI].View
		response.RemoteRecipe = &remote
		_, eligibilityErr := readEligibleFederationRetentionReceipt(r.Context(), tx, objectURI)
		if eligibilityErr == nil {
			canCapture = true
		} else if !errors.Is(eligibilityErr, errRemoteSnapshotNotEligible) {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
	}
	response.SnapshotAvailable = state.State == "active"
	response.CanCaptureSnapshot = canCapture
	response.Saved = true
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) remoteSnapshotByBookmark(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	setPrivateSnapshotHeaders(w)
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	item, err := readRemoteSnapshotForBookmark(r.Context(), tx, userID, bookmarkID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	content, err := parseRemoteSnapshotPayload(item.Payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	response := remoteSnapshotPayloadView{Recipe: content, ObjectURI: item.ObjectURI, SourceOrigin: item.SourceOrigin,
		SourceRecipeID: item.SourceRecipeID, CreatorName: item.CreatorName, Sequence: remoteSnapshotSequenceValue(item.Sequence),
		ReceivedAt: item.ReceivedAt, VersionID: item.VersionID, Provenance: item.Provenance,
		SourceState: item.SourceState}
	if item.Provenance == "publisher_verified" {
		response.RetentionClaim = remoteSnapshotGrantPurpose
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) captureRemoteSnapshotHTTP(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	setPrivateSnapshotHeaders(w)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var response RecipeBookmarkView
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		var createdAt string
		if err := tx.QueryRowContext(r.Context(), `SELECT created_at FROM recipe_bookmarks WHERE id=? AND user_id=? AND target_kind='remote'`, bookmarkID, userID).Scan(&createdAt); errors.Is(err, sql.ErrNoRows) {
			return errRemoteSnapshotNotFound
		} else if err != nil {
			return err
		}
		item, err := captureRemoteSnapshot(r.Context(), tx, userID, bookmarkID, createdAt)
		if err != nil {
			return err
		}
		response = remoteSnapshotBookmarkView(bookmarkID, createdAt, item, "snapshot", "", "", "")
		return nil
	})
	if err != nil {
		writeRemoteSnapshotError(w, err)
		return
	}
	markTransactionalAuditSuccess(r)
	writeJSON(w, http.StatusOK, response)
}

func (h apiHandler) libraryRemoteRecipeDetailGuard(w http.ResponseWriter, r *http.Request) {
	setPrivateSnapshotHeaders(w)
	userID := authenticatedUser(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	values, ok := r.URL.Query()["remote_object_uri"]
	if ok && len(values) == 1 {
		if _, _, err := parseFederationRecipeObjectURI(values[0]); err == nil {
			var blocked int
			err := h.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM recipe_bookmarks b JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id WHERE b.user_id=? AND b.target_kind='remote' AND b.remote_object_uri=? AND p.state IN ('restricted','removed'))`, userID, values[0]).Scan(&blocked)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "storage_error")
				return
			}
			if blocked != 0 {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
		}
	}
	h.libraryRemoteRecipeDetail(w, r)
}
