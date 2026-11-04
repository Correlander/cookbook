package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
)

func (h apiHandler) federationResources(w http.ResponseWriter, r *http.Request) {
	if authenticatedUser(r) == "" {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	allowed, err := canManageFederationIdentity(r.Context(), h.db, authenticatedUser(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage_error")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	switch r.Method {
	case http.MethodGet:
		snapshot, err := readFederationResourceSnapshot(r.Context(), h.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	case http.MethodPatch:
		var input struct {
			GlobalLimits      federationResourceLimits   `json:"global_limits"`
			DefaultPeerLimits federationResourceLimits   `json:"default_peer_limits"`
			Origins           []federationResourceOrigin `json:"origins"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if err := setFederationResourceLimits(r.Context(), h.db, input.GlobalLimits, input.DefaultPeerLimits, input.Origins); err != nil {
			switch {
			case errors.Is(err, errFederationResourceInvalid):
				writeError(w, http.StatusBadRequest, "invalid_resource_limits")
			case errors.Is(err, errFederationResourceInUse):
				writeError(w, http.StatusConflict, "resource_limits_below_usage")
			default:
				writeError(w, http.StatusInternalServerError, "storage_error")
			}
			return
		}
		snapshot, err := readFederationResourceSnapshot(r.Context(), h.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "storage_error")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func readFederationResourceSnapshot(ctx context.Context, db *sql.DB) (federationResourceSnapshot, error) {
	snapshot := federationResourceSnapshot{Origins: []federationResourceOrigin{}}
	var err error
	if snapshot.GlobalLimits, err = readFederationGlobalLimits(ctx, db); err != nil {
		return snapshot, err
	}
	if snapshot.DefaultPeerLimits, err = readFederationDefaultPeerLimits(ctx, db); err != nil {
		return snapshot, err
	}
	if snapshot.GlobalUsage, err = readFederationResourceUsage(ctx, db, "", true); err != nil {
		return snapshot, err
	}
	rows, err := db.QueryContext(ctx, `SELECT origin FROM federation_origin_resource_limits UNION SELECT source_origin FROM federation_remote_catalog UNION SELECT source_origin FROM federation_received_events UNION SELECT source_origin FROM federation_nonces WHERE source_origin<>'' ORDER BY 1`)
	if err != nil {
		return snapshot, err
	}
	origins := []string{}
	for rows.Next() {
		var origin string
		if err := rows.Scan(&origin); err != nil {
			rows.Close()
			return snapshot, err
		}
		origins = append(origins, origin)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	for _, origin := range origins {
		limits, err := readFederationResourceLimits(ctx, db, origin)
		if err != nil {
			return snapshot, err
		}
		usage, err := readFederationResourceUsage(ctx, db, origin, false)
		if err != nil {
			return snapshot, err
		}
		snapshot.Origins = append(snapshot.Origins, federationResourceOrigin{Origin: origin, Limits: limits, Usage: usage})
	}
	sort.Slice(snapshot.Origins, func(i, j int) bool { return snapshot.Origins[i].Origin < snapshot.Origins[j].Origin })
	return snapshot, nil
}
