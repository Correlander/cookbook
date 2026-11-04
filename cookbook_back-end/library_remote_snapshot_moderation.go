package main

import (
	"database/sql"
	"errors"
	"net/http"
)

type remoteSnapshotModerationInput struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason_category"`
}

func (h apiHandler) moderateRemoteSnapshot(w http.ResponseWriter, r *http.Request, bookmarkID string) {
	setPrivateSnapshotHeaders(w)
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	actorID := authenticatedUser(r)
	if actorID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input remoteSnapshotModerationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if (input.Outcome != "restricted" && input.Outcome != "removed") ||
		(input.Reason != "legal" && input.Reason != "security" && input.Reason != "privacy" && input.Reason != "policy") {
		writeError(w, http.StatusBadRequest, "invalid_snapshot_moderation")
		return
	}
	var response RecipeBookmarkView
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		var admin int
		if err := tx.QueryRowContext(r.Context(), `SELECT is_admin FROM users WHERE id=?`, actorID).Scan(&admin); errors.Is(err, sql.ErrNoRows) || (err == nil && admin != 1) {
			return ErrInstanceOwnerUnauthorized
		} else if err != nil {
			return err
		}
		var objectURI, createdAt, versionID, state, moderatedAt, priorReason, priorOutcome string
		if err := tx.QueryRowContext(r.Context(), `SELECT b.remote_object_uri,b.created_at,p.state,COALESCE(p.version_id,p.removed_version_id,''),COALESCE(p.moderated_at,''),COALESCE(p.moderation_reason,''),COALESCE(p.moderation_outcome,'')
			FROM recipe_bookmarks b JOIN library_remote_snapshot_pins p ON p.bookmark_id=b.id
			WHERE b.id=? AND b.target_kind='remote'`, bookmarkID).Scan(&objectURI, &createdAt, &state, &versionID, &moderatedAt, &priorReason, &priorOutcome); errors.Is(err, sql.ErrNoRows) {
			return errRemoteSnapshotNotFound
		} else if err != nil {
			return err
		}
		if state == "removed" {
			return errRemoteSnapshotRemoved
		}
		if state == "restricted" && input.Outcome == "restricted" {
			response = RecipeBookmarkView{ID: bookmarkID, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: objectURI},
				Availability: "unavailable", SaveState: state, VersionID: versionID, ModeratedAt: moderatedAt,
				ModerationReason: priorReason, ModerationOutcome: priorOutcome, Saved: true}
			return nil
		}
		now := timestamp()
		if input.Outcome == "restricted" && state == "active" {
			if _, err := tx.ExecContext(r.Context(), `UPDATE library_remote_snapshot_pins SET state='restricted',moderated_by=?,moderated_at=?,moderation_reason=?,moderation_outcome='restricted' WHERE bookmark_id=?`, actorID, now, input.Reason, bookmarkID); err != nil {
				return err
			}
			state = "restricted"
		} else if input.Outcome == "removed" {
			if _, err := tx.ExecContext(r.Context(), `UPDATE library_remote_snapshot_pins SET state='removed',removed_version_id=version_id,version_id=NULL,moderated_by=?,moderated_at=?,moderation_reason=?,moderation_outcome='removed' WHERE bookmark_id=?`, actorID, now, input.Reason, bookmarkID); err != nil {
				return err
			}
			state = "removed"
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), actorID, "snapshot.moderation."+input.Outcome, "library_snapshot", bookmarkID, http.StatusOK, now); err != nil {
			return err
		}
		response = RecipeBookmarkView{ID: bookmarkID, TargetKind: "remote", Target: &RecipeBookmarkTarget{RemoteObjectURI: objectURI},
			Availability: "unavailable", SaveState: state, VersionID: versionID, ModeratedAt: now,
			ModerationReason: input.Reason, ModerationOutcome: input.Outcome, Saved: true}
		return nil
	})
	if errors.Is(err, ErrInstanceOwnerUnauthorized) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err != nil {
		writeRemoteSnapshotError(w, err)
		return
	}
	markTransactionalAuditSuccess(r)
	writeJSON(w, http.StatusOK, response)
}
