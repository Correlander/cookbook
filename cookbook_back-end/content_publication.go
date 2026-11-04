package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const fixedPolicyTimeLayout = "2006-01-02T15:04:05.000000000Z"

type recipePublicationView struct {
	RecipeID          string                 `json:"recipe_id"`
	PublicationState  RecipePublicationState `json:"publication_state"`
	FederationMode    RecipeFederationMode   `json:"federation_mode"`
	IndexEligible     bool                   `json:"index_eligible"`
	UpdatedBy         *string                `json:"updated_by"`
	UpdatedAt         string                 `json:"updated_at"`
	PublishedAt       *string                `json:"published_at"`
	Visibility        visibility             `json:"visibility"`
	PublicInstance    bool                   `json:"public_instance"`
	FederationEnabled bool                   `json:"federation_enabled"`
	FederationOrigin  string                 `json:"federation_origin"`
}

type recipePublicationRecord struct {
	OwnerID           string
	Deleted           bool
	Visibility        visibility
	PublicInstance    bool
	FederationEnabled bool
	FederationOrigin  string
	Policy            RecipePublicationPolicy
	RawUpdatedAt      string
}

type recipePublicationInput struct {
	PublicationState RecipePublicationState `json:"publication_state"`
	FederationMode   RecipeFederationMode   `json:"federation_mode"`
	IndexEligible    bool                   `json:"index_eligible"`
}

func (input *recipePublicationInput) UnmarshalJSON(data []byte) error {
	var fields struct {
		PublicationState *RecipePublicationState `json:"publication_state"`
		FederationMode   *RecipeFederationMode   `json:"federation_mode"`
		IndexEligible    *bool                   `json:"index_eligible"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	if fields.PublicationState == nil || fields.FederationMode == nil || fields.IndexEligible == nil {
		return errors.New("publication policy requires all fields")
	}
	input.PublicationState = *fields.PublicationState
	input.FederationMode = *fields.FederationMode
	input.IndexEligible = *fields.IndexEligible
	return nil
}

type recipeCreateInput struct {
	recipeInput
	ConfirmPublicAccess bool                           `json:"confirm_public_access"`
	InitialPublication  *initialRecipePublicationInput `json:"initial_publication"`
}

type recipeUpdateInput struct {
	recipeInput
	ConfirmPublicAccess bool `json:"confirm_public_access"`
}

type revisionRestoreInput struct {
	ExpectedVersion     int  `json:"expected_version"`
	ConfirmPublicAccess bool `json:"confirm_public_access"`
}

type initialRecipePublicationInput struct {
	PublicationState RecipePublicationState `json:"publication_state"`
	FederationMode   RecipeFederationMode   `json:"federation_mode"`
	IndexEligible    bool                   `json:"index_eligible"`
}

func (input *initialRecipePublicationInput) UnmarshalJSON(data []byte) error {
	var fields struct {
		PublicationState *RecipePublicationState `json:"publication_state"`
		FederationMode   *RecipeFederationMode   `json:"federation_mode"`
		IndexEligible    *bool                   `json:"index_eligible"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	if fields.PublicationState == nil || fields.FederationMode == nil || fields.IndexEligible == nil {
		return errors.New("initial publication requires all fields")
	}
	input.PublicationState = *fields.PublicationState
	input.FederationMode = *fields.FederationMode
	input.IndexEligible = *fields.IndexEligible
	return nil
}

type recipePublicationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func publicationFixedTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("invalid recipe publication timestamp")
	}
	return parsed.UTC().Format(fixedPolicyTimeLayout), nil
}

func nextPublicationTime(previous string) (string, error) {
	now := time.Now().UTC()
	if previous != "" {
		parsed, err := time.Parse(time.RFC3339Nano, previous)
		if err != nil {
			return "", errors.New("invalid recipe publication timestamp")
		}
		next := parsed.UTC().Add(time.Nanosecond)
		if next.After(now) {
			now = next
		}
	}
	return now.Format(fixedPolicyTimeLayout), nil
}

func readRecipePublicationRecord(ctx context.Context, queryer recipePublicationQueryer, recipeID string) (recipePublicationRecord, error) {
	var record recipePublicationRecord
	var deleted, publicInstance, federationEnabled int
	var state, mode, updatedAt sql.NullString
	var indexEligible sql.NullInt64
	if err := queryer.QueryRowContext(ctx, `SELECT r.owner_id,r.deleted_at IS NOT NULL,r.visibility,s.public_instance,s.federation_enabled,s.federation_origin,
		p.publication_state,p.federation_mode,p.index_eligible,p.updated_by,p.updated_at,p.published_at
		FROM recipes r JOIN instance_settings s ON s.id=1 LEFT JOIN recipe_publication_policy p ON p.recipe_id=r.id WHERE r.id=?`, recipeID).
		Scan(&record.OwnerID, &deleted, &record.Visibility, &publicInstance, &federationEnabled, &record.FederationOrigin,
			&state, &mode, &indexEligible, &record.Policy.UpdatedBy, &updatedAt, &record.Policy.PublishedAt); err != nil {
		return recipePublicationRecord{}, err
	}
	record.Deleted = deleted == 1
	record.PublicInstance = publicInstance == 1
	record.FederationEnabled = federationEnabled == 1
	if !state.Valid || !mode.Valid || !indexEligible.Valid || !updatedAt.Valid || (indexEligible.Int64 != 0 && indexEligible.Int64 != 1) {
		return record, errors.New("missing or corrupt recipe publication policy")
	}
	record.Policy.RecipeID = recipeID
	record.Policy.PublicationState = RecipePublicationState(state.String)
	record.Policy.FederationMode = RecipeFederationMode(mode.String)
	record.Policy.IndexEligible = indexEligible.Int64 == 1
	record.RawUpdatedAt = updatedAt.String
	record.Policy.UpdatedAt = updatedAt.String
	if err := validateRecipePublicationPolicy(record.Policy); err != nil {
		return record, fmt.Errorf("corrupt recipe publication policy: %w", err)
	}
	if _, err := publicationFixedTime(updatedAt.String); err != nil {
		return record, err
	}
	if record.Policy.PublishedAt.Valid {
		if _, err := publicationFixedTime(record.Policy.PublishedAt.String); err != nil {
			return record, err
		}
	}
	return record, nil
}

func (record recipePublicationRecord) view() (recipePublicationView, error) {
	updatedAt, err := publicationFixedTime(record.RawUpdatedAt)
	if err != nil {
		return recipePublicationView{}, err
	}
	view := recipePublicationView{
		RecipeID: record.Policy.RecipeID, PublicationState: record.Policy.PublicationState,
		FederationMode: record.Policy.FederationMode, IndexEligible: record.Policy.IndexEligible,
		UpdatedAt: updatedAt, Visibility: record.Visibility, PublicInstance: record.PublicInstance,
		FederationEnabled: record.FederationEnabled, FederationOrigin: record.FederationOrigin,
	}
	if record.Policy.UpdatedBy.Valid {
		value := record.Policy.UpdatedBy.String
		view.UpdatedBy = &value
	}
	if record.Policy.PublishedAt.Valid {
		value, err := publicationFixedTime(record.Policy.PublishedAt.String)
		if err != nil {
			return recipePublicationView{}, err
		}
		view.PublishedAt = &value
	}
	return view, nil
}

func publicationEntityTag(view recipePublicationView) (string, []byte, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(body)
	return `"sha256:` + hex.EncodeToString(digest[:]) + `"`, body, nil
}

func writeRecipePublicationView(w http.ResponseWriter, view recipePublicationView) error {
	etag, body, err := publicationEntityTag(view)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store, no-transform")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(body)
	return err
}

func parsePublicationIfMatch(r *http.Request) (string, int, string) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return "", http.StatusPreconditionRequired, "precondition_required"
	}
	if len(values) != 1 || len(values[0]) != len(`"sha256:`)+64+1 ||
		!strings.HasPrefix(values[0], `"sha256:`) || !strings.HasSuffix(values[0], `"`) {
		return "", http.StatusBadRequest, "invalid_precondition"
	}
	digest := values[0][len(`"sha256:`) : len(values[0])-1]
	if len(digest) != 64 {
		return "", http.StatusBadRequest, "invalid_precondition"
	}
	for _, char := range digest {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return "", http.StatusBadRequest, "invalid_precondition"
		}
	}
	return values[0], 0, ""
}

func insertInitialRecipePublicationPolicy(ctx context.Context, tx *sql.Tx, recipeID, ownerID string, input initialRecipePublicationInput, now string) error {
	policy := RecipePublicationPolicy{
		RecipeID: recipeID, PublicationState: input.PublicationState,
		FederationMode: input.FederationMode, IndexEligible: input.IndexEligible,
		UpdatedBy: sql.NullString{String: ownerID, Valid: true}, UpdatedAt: now,
	}
	if input.PublicationState == RecipePublicationPublished {
		policy.PublishedAt = sql.NullString{String: now, Valid: true}
	}
	if err := validateRecipePublicationPolicy(policy); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO recipe_publication_policy
		(recipe_id,publication_state,federation_mode,index_eligible,updated_by,updated_at,published_at)
		VALUES(?,?,?,?,?,?,?)`, policy.RecipeID, policy.PublicationState, policy.FederationMode,
		policy.IndexEligible, policy.UpdatedBy.String, policy.UpdatedAt, nullablePolicyString(policy.PublishedAt))
	return err
}

func validateRecipeCreationGates(ctx context.Context, tx *sql.Tx, visibilityValue visibility, confirmPublicAccess bool, initial initialRecipePublicationInput) string {
	if visibilityValue == visibilityPublic && !confirmPublicAccess {
		return "publication_confirmation_required"
	}
	var publicInstance, federationEnabled int
	var origin string
	if err := tx.QueryRowContext(ctx, `SELECT public_instance,federation_enabled,federation_origin FROM instance_settings WHERE id=1`).Scan(&publicInstance, &federationEnabled, &origin); err != nil {
		return "storage_error"
	}
	if visibilityValue != visibilityPrivate && publicInstance != 1 {
		return "instance_private"
	}
	if initial.PublicationState == RecipePublicationPublished && (visibilityValue != visibilityPublic || publicInstance != 1) {
		return "publication_unavailable"
	}
	if initial.FederationMode == RecipeFederationFollowers && (initial.PublicationState != RecipePublicationPublished || visibilityValue != visibilityPublic || publicInstance != 1 || federationEnabled != 1 || !validFederationEgressOrigin(origin)) {
		return "federation_unavailable"
	}
	return ""
}

func nullablePolicyString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func validateInitialPublication(input initialRecipePublicationInput, provided bool) (initialRecipePublicationInput, string) {
	if !provided {
		return initialRecipePublicationInput{PublicationState: RecipePublicationUnpublished, FederationMode: RecipeFederationOff}, ""
	}
	if input.IndexEligible {
		return input, "index_submission_unavailable"
	}
	policy := RecipePublicationPolicy{RecipeID: "validate", PublicationState: input.PublicationState, FederationMode: input.FederationMode, IndexEligible: input.IndexEligible}
	if input.PublicationState == RecipePublicationWithdrawn || validateRecipePublicationPolicy(policy) != nil {
		return input, "invalid_publication_policy"
	}
	return input, ""
}

func validatePublicationChange(current recipePublicationRecord, input recipePublicationInput) string {
	policy := RecipePublicationPolicy{RecipeID: current.Policy.RecipeID, PublicationState: input.PublicationState, FederationMode: input.FederationMode, IndexEligible: input.IndexEligible}
	if input.IndexEligible {
		return "index_submission_unavailable"
	}
	if err := validateRecipePublicationPolicy(policy); err != nil {
		return "invalid_publication_policy"
	}
	switch current.Policy.PublicationState {
	case RecipePublicationUnpublished:
		if input.PublicationState != RecipePublicationUnpublished && input.PublicationState != RecipePublicationPublished {
			return "invalid_publication_policy"
		}
	case RecipePublicationPublished:
		if input.PublicationState == RecipePublicationUnpublished {
			return "invalid_publication_policy"
		}
	case RecipePublicationWithdrawn:
		if input.PublicationState != RecipePublicationWithdrawn && input.PublicationState != RecipePublicationPublished {
			return "invalid_publication_policy"
		}
	default:
		return "invalid_publication_policy"
	}
	if current.Policy.PublicationState != RecipePublicationPublished && input.PublicationState == RecipePublicationPublished &&
		(current.Visibility != visibilityPublic || !current.PublicInstance) {
		return "publication_unavailable"
	}
	if current.Policy.FederationMode != RecipeFederationFollowers && input.FederationMode == RecipeFederationFollowers {
		if input.PublicationState != RecipePublicationPublished || current.Visibility != visibilityPublic || !current.PublicInstance ||
			!current.FederationEnabled || !validFederationEgressOrigin(current.FederationOrigin) {
			return "federation_unavailable"
		}
	}
	return ""
}

func publicationAuditActions(old RecipePublicationPolicy, input recipePublicationInput) []string {
	actions := make([]string, 0, 2)
	switch {
	case old.PublicationState != RecipePublicationPublished && input.PublicationState == RecipePublicationPublished:
		if old.PublicationState == RecipePublicationWithdrawn {
			actions = append(actions, "recipe.publication.republish")
		} else {
			actions = append(actions, "recipe.publication.publish")
		}
	case old.PublicationState == RecipePublicationPublished && input.PublicationState == RecipePublicationWithdrawn:
		actions = append(actions, "recipe.publication.withdraw")
	}
	if old.FederationMode != input.FederationMode {
		if input.FederationMode == RecipeFederationFollowers {
			actions = append(actions, "recipe.federation.enable_followers")
		} else {
			actions = append(actions, "recipe.federation.disable_followers")
		}
	}
	return actions
}

func (h apiHandler) recipePublication(w http.ResponseWriter, r *http.Request, recipeID string) {
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		record, err := readRecipePublicationRecord(r.Context(), h.db, recipeID)
		if errors.Is(err, sql.ErrNoRows) || (record.OwnerID != "" && (record.Deleted || record.OwnerID != uid)) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		view, err := record.view()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if err := writeRecipePublicationView(w, view); err != nil {
			return
		}
	case http.MethodPut:
		h.updateRecipePublication(w, r, recipeID, uid)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (h apiHandler) federationRetentionAssertion(w http.ResponseWriter, r *http.Request, recipeID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	uid := authenticatedUser(r)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var input struct {
		ExpectedRecipeVersion int `json:"expected_recipe_version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedRecipeVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_recipe_version")
		return
	}
	err := withImmediateSQLiteWriteTx(r.Context(), h.db, func(tx sqliteExecutor) error {
		if err := enqueueFederationOwnerAssertion(r.Context(), tx, recipeID, uid, input.ExpectedRecipeVersion); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(), `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), uid, "recipe.federation.retention.assert", "recipes", recipeID, http.StatusAccepted, timestamp())
		return err
	})
	if errors.Is(err, errFederationOwnerAssertionConflict) {
		writeError(w, http.StatusConflict, "federation_assertion_conflict")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "outbox capacity") {
			w.Header().Set("Retry-After", "3600")
			writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	markTransactionalAuditSuccess(r)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (h apiHandler) updateRecipePublication(w http.ResponseWriter, r *http.Request, recipeID, uid string) {
	ifMatch, status, code := parsePublicationIfMatch(r)
	if status != 0 {
		writeError(w, status, code)
		return
	}
	var input recipePublicationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	defer tx.Rollback()
	record, err := readRecipePublicationRecord(r.Context(), tx, recipeID)
	if errors.Is(err, sql.ErrNoRows) || (record.OwnerID != "" && (record.Deleted || record.OwnerID != uid)) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	currentView, err := record.view()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	currentETag, _, err := publicationEntityTag(currentView)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if ifMatch != currentETag {
		w.Header().Set("Cache-Control", "private, no-store, no-transform")
		w.Header().Set("ETag", currentETag)
		writeError(w, http.StatusPreconditionFailed, "precondition_failed")
		return
	}
	if input.IndexEligible {
		writeError(w, http.StatusConflict, "index_submission_unavailable")
		return
	}
	if input.PublicationState == record.Policy.PublicationState && input.FederationMode == record.Policy.FederationMode && input.IndexEligible == record.Policy.IndexEligible {
		if err := tx.Rollback(); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		markTransactionalAuditSuccess(r)
		if err := writeRecipePublicationView(w, currentView); err != nil {
			return
		}
		return
	}
	if code := validatePublicationChange(record, input); code != "" {
		writeError(w, publicationInputStatus(code), code)
		return
	}
	updatedAt, err := nextPublicationTime(record.RawUpdatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	publishedAt := record.Policy.PublishedAt
	if record.Policy.PublicationState != RecipePublicationPublished && input.PublicationState == RecipePublicationPublished {
		publishedAt = sql.NullString{String: updatedAt, Valid: true}
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE recipe_publication_policy SET publication_state=?,federation_mode=?,index_eligible=?,updated_by=?,updated_at=?,published_at=?
		WHERE recipe_id=? AND publication_state=? AND federation_mode=? AND index_eligible=? AND updated_by IS ? AND updated_at=? AND published_at IS ?
		AND EXISTS(SELECT 1 FROM recipes WHERE id=? AND owner_id=? AND deleted_at IS NULL AND visibility=?)
		AND EXISTS(SELECT 1 FROM instance_settings WHERE id=1 AND public_instance=? AND federation_enabled=? AND federation_origin=?)`,
		input.PublicationState, input.FederationMode, input.IndexEligible, uid, updatedAt, nullablePolicyString(publishedAt),
		recipeID, record.Policy.PublicationState, record.Policy.FederationMode, record.Policy.IndexEligible,
		nullablePolicyString(record.Policy.UpdatedBy), record.RawUpdatedAt, nullablePolicyString(record.Policy.PublishedAt),
		recipeID, uid, record.Visibility, boolInt(record.PublicInstance), boolInt(record.FederationEnabled), record.FederationOrigin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if changed != 1 {
		latest, readErr := readRecipePublicationRecord(r.Context(), tx, recipeID)
		if errors.Is(readErr, sql.ErrNoRows) || (latest.OwnerID != "" && (latest.Deleted || latest.OwnerID != uid)) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		if gateCode := validatePublicationChange(latest, input); gateCode == "publication_unavailable" || gateCode == "federation_unavailable" || gateCode == "index_submission_unavailable" {
			writePublicationError(w, gateCode)
			return
		}
		latestView, viewErr := latest.view()
		if viewErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		latestETag, _, tagErr := publicationEntityTag(latestView)
		if tagErr != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		w.Header().Set("ETag", latestETag)
		w.Header().Set("Cache-Control", "private, no-store, no-transform")
		writeError(w, http.StatusPreconditionFailed, "precondition_failed")
		return
	}
	for _, action := range publicationAuditActions(record.Policy, input) {
		if err := insertAuditEvent(tx, uid, action, "recipes", recipeID, http.StatusOK); err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
	}
	if err := enqueueFederationChange(tx, recipeID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "federation_queue_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	markTransactionalAuditSuccess(r)
	record.Policy.PublicationState = input.PublicationState
	record.Policy.FederationMode = input.FederationMode
	record.Policy.IndexEligible = input.IndexEligible
	record.Policy.UpdatedBy = sql.NullString{String: uid, Valid: true}
	record.Policy.UpdatedAt = updatedAt
	record.RawUpdatedAt = updatedAt
	record.Policy.PublishedAt = publishedAt
	updatedView, err := record.view()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if err := writeRecipePublicationView(w, updatedView); err != nil {
		return
	}
}

func publicationInputStatus(code string) int {
	switch code {
	case "index_submission_unavailable", "publication_unavailable", "federation_unavailable":
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func writePublicationError(w http.ResponseWriter, code string) {
	status := publicationInputStatus(code)
	switch code {
	case "publication_confirmation_required":
		status = http.StatusConflict
	case "instance_private":
		status = http.StatusForbidden
	case "storage_error":
		status = http.StatusInternalServerError
	}
	writeError(w, status, code)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func advanceLifecyclePolicy(ctx context.Context, tx *sql.Tx, record recipePublicationRecord, actorID string) error {
	updatedAt, err := nextPublicationTime(record.RawUpdatedAt)
	if err != nil {
		return err
	}
	state := record.Policy.PublicationState
	if state == RecipePublicationPublished {
		state = RecipePublicationWithdrawn
	}
	result, err := tx.ExecContext(ctx, `UPDATE recipe_publication_policy SET publication_state=?,federation_mode='off',index_eligible=0,updated_by=?,updated_at=?
		WHERE recipe_id=? AND publication_state=? AND federation_mode=? AND index_eligible=? AND updated_by IS ? AND updated_at=? AND published_at IS ?`,
		state, actorID, updatedAt, record.Policy.RecipeID, record.Policy.PublicationState, record.Policy.FederationMode,
		record.Policy.IndexEligible, nullablePolicyString(record.Policy.UpdatedBy), record.RawUpdatedAt, nullablePolicyString(record.Policy.PublishedAt))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("recipe publication policy changed during lifecycle action")
	}
	return nil
}
