package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

var errPublisherProfileRequired = errors.New("completed publisher profile required")

// requireCompletedPublisherProfile checks only the authenticated account's
// completion state. Profile visibility and profile contents are not publication
// consent and are intentionally not returned to callers.
func requireCompletedPublisherProfile(ctx context.Context, queryer contentQueryer, accountID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if accountID == "" {
		return errPublisherProfileRequired
	}
	var publicName string
	err := queryer.QueryRowContext(ctx, `SELECT public_name FROM creator_profiles WHERE user_id=?`, accountID).Scan(&publicName)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(publicName) == "") {
		return errPublisherProfileRequired
	}
	if err != nil {
		return err
	}
	return nil
}

func requirePublisherProfilesForOwners(ctx context.Context, queryer contentQueryer, ownerQuery string) error {
	rows, err := queryer.QueryContext(ctx, ownerQuery)
	if err != nil {
		return err
	}
	owners := make([]string, 0)
	for rows.Next() {
		var ownerID string
		if err := rows.Scan(&ownerID); err != nil {
			rows.Close()
			return err
		}
		owners = append(owners, ownerID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, ownerID := range owners {
		if err := requireCompletedPublisherProfile(ctx, queryer, ownerID); err != nil {
			return err
		}
	}
	return nil
}

func writePublisherProfileError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errPublisherProfileRequired) {
		return false
	}
	writeError(w, http.StatusConflict, "publisher_profile_required")
	return true
}
