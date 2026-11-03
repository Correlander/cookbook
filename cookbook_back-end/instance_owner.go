package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const instanceOwnerSchemaDDL = `CREATE TABLE IF NOT EXISTS instance_authority (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  owner_user_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
  owner_claimed_at TEXT,
  generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
  CHECK (
    (owner_user_id IS NULL AND owner_claimed_at IS NULL AND generation = 0)
    OR (owner_user_id IS NOT NULL AND owner_claimed_at IS NOT NULL AND generation >= 1)
  )
);
INSERT OR IGNORE INTO instance_authority(singleton_id) VALUES (1);
CREATE TABLE IF NOT EXISTS instance_owner_transfers (
  id TEXT PRIMARY KEY,
  from_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  to_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK (state IN ('pending','accepted','cancelled','expired')),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (from_user_id <> to_user_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS one_pending_owner_transfer
  ON instance_owner_transfers(state) WHERE state = 'pending';`

const instanceOwnerTransferLifetime = 7 * 24 * time.Hour

var (
	ErrInstanceAuthorityMissing  = errors.New("instance authority record is missing")
	ErrInstanceOwnerAlreadySet   = errors.New("instance owner is already claimed")
	ErrInstanceOwnerUnclaimed    = errors.New("instance owner is not claimed")
	ErrInstanceOwnerUnauthorized = errors.New("actor is not the instance owner")
	ErrOwnerAccountNotFound      = errors.New("owner account does not exist")
	ErrOwnerTransferNotFound     = errors.New("owner transfer does not exist")
	ErrOwnerTransferPending      = errors.New("an owner transfer is already pending")
	ErrOwnerTransferSelf         = errors.New("owner cannot transfer authority to itself")
	ErrOwnerTransferNotPending   = errors.New("owner transfer is not pending")
	ErrOwnerTransferExpired      = errors.New("owner transfer has expired")
	ErrOwnerTransferStale        = errors.New("owner transfer no longer matches current authority")
)

type OwnerTransferState string

const (
	OwnerTransferPending   OwnerTransferState = "pending"
	OwnerTransferAccepted  OwnerTransferState = "accepted"
	OwnerTransferCancelled OwnerTransferState = "cancelled"
	OwnerTransferExpired   OwnerTransferState = "expired"
)

type InstanceAuthority struct {
	OwnerUserID string
	ClaimedAt   time.Time
	Generation  int64
}

func (a InstanceAuthority) Claimed() bool { return a.OwnerUserID != "" }

type InstanceOwnerCapabilities struct {
	IsInstanceOwner bool
	IsAdmin         bool
	OwnerClaimed    bool
	Generation      int64
}

type InstanceOwnerTransfer struct {
	ID          string
	FromUserID  string
	ToUserID    string
	State       OwnerTransferState
	CreatedAt   time.Time
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

// ApplyInstanceOwnerDDL is the reusable version-2 migration body. The caller
// must run it inside the migration transaction so the schema and ledger commit
// atomically.
func ApplyInstanceOwnerDDL(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, instanceOwnerSchemaDDL); err != nil {
		return fmt.Errorf("apply instance owner schema: %w", err)
	}
	return nil
}

// InstanceOwnerCapabilitiesFor returns fixed role/capability facts for an
// existing local account. Owner status never implies admin or content access.
func InstanceOwnerCapabilitiesFor(ctx context.Context, db *sql.DB, accountID string) (InstanceOwnerCapabilities, error) {
	if strings.TrimSpace(accountID) == "" {
		return InstanceOwnerCapabilities{}, ErrOwnerAccountNotFound
	}
	var isAdmin int
	var ownerUserID sql.NullString
	var generation int64
	err := db.QueryRowContext(ctx, `SELECT u.is_admin,a.owner_user_id,a.generation
		FROM users u CROSS JOIN instance_authority a
		WHERE u.id=? AND a.singleton_id=1`, accountID).Scan(&isAdmin, &ownerUserID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		var authorityExists int
		if checkErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_authority WHERE singleton_id=1`).Scan(&authorityExists); checkErr != nil {
			return InstanceOwnerCapabilities{}, fmt.Errorf("read instance authority: %w", checkErr)
		}
		if authorityExists == 0 {
			return InstanceOwnerCapabilities{}, ErrInstanceAuthorityMissing
		}
		return InstanceOwnerCapabilities{}, ErrOwnerAccountNotFound
	}
	if err != nil {
		return InstanceOwnerCapabilities{}, fmt.Errorf("read instance owner capabilities: %w", err)
	}
	return InstanceOwnerCapabilities{
		IsInstanceOwner: ownerUserID.Valid && ownerUserID.String == accountID,
		IsAdmin:         isAdmin == 1,
		OwnerClaimed:    ownerUserID.Valid,
		Generation:      generation,
	}, nil
}

// ClaimInstanceOwner explicitly claims the unclaimed instance for an existing
// local account. It never changes that account's administrator bit.
func ClaimInstanceOwner(ctx context.Context, db *sql.DB, accountID string) (InstanceAuthority, error) {
	return claimInstanceOwnerWithClock(ctx, db, accountID, time.Now)
}

func claimInstanceOwnerAt(ctx context.Context, db *sql.DB, accountID string, now time.Time) (authority InstanceAuthority, err error) {
	return claimInstanceOwnerWithClock(ctx, db, accountID, func() time.Time { return now })
}

func claimInstanceOwnerWithClock(ctx context.Context, db *sql.DB, accountID string, clock func() time.Time) (authority InstanceAuthority, err error) {
	if strings.TrimSpace(accountID) == "" {
		return InstanceAuthority{}, ErrOwnerAccountNotFound
	}
	err = withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		now := clock().UTC()
		if err := requireOwnerAccount(ctx, tx, accountID); err != nil {
			return err
		}
		existing, err := readInstanceAuthority(ctx, tx)
		if err != nil {
			return err
		}
		if existing.Claimed() {
			return ErrInstanceOwnerAlreadySet
		}
		result, err := tx.ExecContext(ctx, `UPDATE instance_authority SET owner_user_id=?,owner_claimed_at=?,generation=1 WHERE singleton_id=1 AND owner_user_id IS NULL AND generation=0`, accountID, formatOwnerTime(now))
		if err != nil {
			return fmt.Errorf("claim instance owner: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify instance owner claim: %w", err)
		}
		if changed != 1 {
			return ErrInstanceOwnerAlreadySet
		}
		if err := insertInstanceOwnerAudit(ctx, tx, accountID, "instance_owner.claim", "instance_authority", "1", http.StatusOK, now); err != nil {
			return err
		}
		authority, err = readInstanceAuthority(ctx, tx)
		return err
	})
	return authority, err
}

// CreateInstanceOwnerTransfer creates one expiring request from the current
// owner to an existing, different local account.
func CreateInstanceOwnerTransfer(ctx context.Context, db *sql.DB, actorID, targetID string) (InstanceOwnerTransfer, error) {
	return createInstanceOwnerTransferWithClock(ctx, db, actorID, targetID, time.Now)
}

func createInstanceOwnerTransferAt(ctx context.Context, db *sql.DB, actorID, targetID string, now time.Time) (transfer InstanceOwnerTransfer, err error) {
	return createInstanceOwnerTransferWithClock(ctx, db, actorID, targetID, func() time.Time { return now })
}

func createInstanceOwnerTransferWithClock(ctx context.Context, db *sql.DB, actorID, targetID string, clock func() time.Time) (transfer InstanceOwnerTransfer, err error) {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(targetID) == "" {
		return InstanceOwnerTransfer{}, ErrOwnerAccountNotFound
	}
	err = withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		now := clock().UTC()
		authority, err := readInstanceAuthority(ctx, tx)
		if err != nil {
			return err
		}
		if !authority.Claimed() {
			return ErrInstanceOwnerUnclaimed
		}
		if authority.OwnerUserID != actorID {
			return ErrInstanceOwnerUnauthorized
		}
		if actorID == targetID {
			return ErrOwnerTransferSelf
		}
		if err := requireOwnerAccount(ctx, tx, targetID); err != nil {
			return err
		}
		if _, err := expireInstanceOwnerTransfersTx(ctx, tx, now); err != nil {
			return err
		}
		id := newID()
		expiresAt := now.Add(instanceOwnerTransferLifetime)
		if _, err := tx.ExecContext(ctx, `INSERT INTO instance_owner_transfers(id,from_user_id,to_user_id,state,created_at,expires_at) VALUES(?,?,?,'pending',?,?)`, id, actorID, targetID, formatOwnerTime(now), formatOwnerTime(expiresAt)); err != nil {
			var pending int
			if checkErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_owner_transfers WHERE state='pending'`).Scan(&pending); checkErr == nil && pending > 0 {
				return ErrOwnerTransferPending
			}
			return fmt.Errorf("create owner transfer: %w", err)
		}
		if err := insertInstanceOwnerAudit(ctx, tx, actorID, "instance_owner.transfer.create", "instance_owner_transfer", id, http.StatusCreated, now); err != nil {
			return err
		}
		transfer = InstanceOwnerTransfer{ID: id, FromUserID: actorID, ToUserID: targetID, State: OwnerTransferPending, CreatedAt: now.UTC(), ExpiresAt: expiresAt.UTC()}
		return nil
	})
	return transfer, err
}

// AcceptInstanceOwnerTransfer atomically changes the authority to the target,
// advances generation, completes the request, and records audit.
func AcceptInstanceOwnerTransfer(ctx context.Context, db *sql.DB, actorID, transferID string) (InstanceAuthority, error) {
	return acceptInstanceOwnerTransferWithClock(ctx, db, actorID, transferID, time.Now)
}

func acceptInstanceOwnerTransferAt(ctx context.Context, db *sql.DB, actorID, transferID string, now time.Time) (authority InstanceAuthority, err error) {
	return acceptInstanceOwnerTransferWithClock(ctx, db, actorID, transferID, func() time.Time { return now })
}

func acceptInstanceOwnerTransferWithClock(ctx context.Context, db *sql.DB, actorID, transferID string, clock func() time.Time) (authority InstanceAuthority, err error) {
	expired := false
	err = withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		now := clock().UTC()
		transfer, err := readInstanceOwnerTransfer(ctx, tx, transferID)
		if err != nil {
			return err
		}
		if transfer.State != OwnerTransferPending {
			return ErrOwnerTransferNotPending
		}
		if !now.Before(transfer.ExpiresAt) {
			if err := setOwnerTransferTerminal(ctx, tx, transfer.ID, OwnerTransferExpired, now); err != nil {
				return err
			}
			if err := insertInstanceOwnerAudit(ctx, tx, "", "instance_owner.transfer.expire", "instance_owner_transfer", transfer.ID, http.StatusGone, now); err != nil {
				return err
			}
			expired = true
			return nil
		}
		if actorID != transfer.ToUserID {
			return ErrInstanceOwnerUnauthorized
		}
		currentAuthority, err := readInstanceAuthority(ctx, tx)
		if err != nil {
			return err
		}
		if !currentAuthority.Claimed() || currentAuthority.OwnerUserID != transfer.FromUserID {
			return ErrOwnerTransferStale
		}
		result, err := tx.ExecContext(ctx, `UPDATE instance_authority SET owner_user_id=?,generation=generation+1 WHERE singleton_id=1 AND owner_user_id=? AND generation=?`, transfer.ToUserID, transfer.FromUserID, currentAuthority.Generation)
		if err != nil {
			return fmt.Errorf("accept owner transfer: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("verify owner transfer acceptance: %w", err)
		}
		if changed != 1 {
			return ErrOwnerTransferStale
		}
		if err := setOwnerTransferTerminal(ctx, tx, transfer.ID, OwnerTransferAccepted, now); err != nil {
			return err
		}
		if err := insertInstanceOwnerAudit(ctx, tx, actorID, "instance_owner.transfer.accept", "instance_owner_transfer", transfer.ID, http.StatusOK, now); err != nil {
			return err
		}
		authority, err = readInstanceAuthority(ctx, tx)
		return err
	})
	if err == nil && expired {
		return InstanceAuthority{}, ErrOwnerTransferExpired
	}
	return authority, err
}

// CancelInstanceOwnerTransfer permits only the current initiating owner to
// cancel a still-pending request.
func CancelInstanceOwnerTransfer(ctx context.Context, db *sql.DB, actorID, transferID string) error {
	return cancelInstanceOwnerTransferWithClock(ctx, db, actorID, transferID, time.Now)
}

func cancelInstanceOwnerTransferAt(ctx context.Context, db *sql.DB, actorID, transferID string, now time.Time) error {
	return cancelInstanceOwnerTransferWithClock(ctx, db, actorID, transferID, func() time.Time { return now })
}

func cancelInstanceOwnerTransferWithClock(ctx context.Context, db *sql.DB, actorID, transferID string, clock func() time.Time) error {
	expired := false
	err := withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		now := clock().UTC()
		transfer, err := readInstanceOwnerTransfer(ctx, tx, transferID)
		if err != nil {
			return err
		}
		if transfer.State != OwnerTransferPending {
			return ErrOwnerTransferNotPending
		}
		if !now.Before(transfer.ExpiresAt) {
			if err := setOwnerTransferTerminal(ctx, tx, transfer.ID, OwnerTransferExpired, now); err != nil {
				return err
			}
			if err := insertInstanceOwnerAudit(ctx, tx, "", "instance_owner.transfer.expire", "instance_owner_transfer", transfer.ID, http.StatusGone, now); err != nil {
				return err
			}
			expired = true
			return nil
		}
		authority, err := readInstanceAuthority(ctx, tx)
		if err != nil {
			return err
		}
		if !authority.Claimed() {
			return ErrInstanceOwnerUnclaimed
		}
		if authority.OwnerUserID != actorID || transfer.FromUserID != actorID {
			return ErrInstanceOwnerUnauthorized
		}
		if err := setOwnerTransferTerminal(ctx, tx, transfer.ID, OwnerTransferCancelled, now); err != nil {
			return err
		}
		return insertInstanceOwnerAudit(ctx, tx, actorID, "instance_owner.transfer.cancel", "instance_owner_transfer", transfer.ID, http.StatusOK, now)
	})
	if err == nil && expired {
		return ErrOwnerTransferExpired
	}
	return err
}

// ExpireInstanceOwnerTransfers finalizes due requests without an application
// actor. It is safe to call repeatedly; only transitions are audited/countable.
func ExpireInstanceOwnerTransfers(ctx context.Context, db *sql.DB) (int64, error) {
	return expireInstanceOwnerTransfersWithClock(ctx, db, time.Now)
}

func expireInstanceOwnerTransfersAt(ctx context.Context, db *sql.DB, now time.Time) (int64, error) {
	return expireInstanceOwnerTransfersWithClock(ctx, db, func() time.Time { return now })
}

func expireInstanceOwnerTransfersWithClock(ctx context.Context, db *sql.DB, clock func() time.Time) (int64, error) {
	var expired int64
	err := withInstanceOwnerWriteTx(ctx, db, func(tx migrationExecutor) error {
		var err error
		expired, err = expireInstanceOwnerTransfersTx(ctx, tx, clock().UTC())
		return err
	})
	return expired, err
}

func expireInstanceOwnerTransfersTx(ctx context.Context, tx migrationExecutor, now time.Time) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,expires_at FROM instance_owner_transfers WHERE state='pending' ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("list expired owner transfers: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id, expiresAt string
		if err := rows.Scan(&id, &expiresAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read expired owner transfer: %w", err)
		}
		deadline, err := time.Parse(time.RFC3339Nano, expiresAt)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("parse owner transfer expiry time: %w", err)
		}
		if !now.Before(deadline) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read expired owner transfers: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close expired owner transfers: %w", err)
	}
	var count int64
	for _, id := range ids {
		if err := setOwnerTransferTerminal(ctx, tx, id, OwnerTransferExpired, now); err != nil {
			return 0, err
		}
		if err := insertInstanceOwnerAudit(ctx, tx, "", "instance_owner.transfer.expire", "instance_owner_transfer", id, http.StatusGone, now); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func readInstanceAuthority(ctx context.Context, exec migrationExecutor) (InstanceAuthority, error) {
	var ownerID, claimedAt sql.NullString
	var generation int64
	err := exec.QueryRowContext(ctx, `SELECT owner_user_id,owner_claimed_at,generation FROM instance_authority WHERE singleton_id=1`).Scan(&ownerID, &claimedAt, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceAuthority{}, ErrInstanceAuthorityMissing
	}
	if err != nil {
		return InstanceAuthority{}, fmt.Errorf("read instance authority: %w", err)
	}
	authority := InstanceAuthority{Generation: generation}
	if ownerID.Valid {
		authority.OwnerUserID = ownerID.String
	}
	if claimedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, claimedAt.String)
		if err != nil {
			return InstanceAuthority{}, fmt.Errorf("parse instance owner claim time: %w", err)
		}
		authority.ClaimedAt = parsed.UTC()
	}
	return authority, nil
}

func readInstanceOwnerTransfer(ctx context.Context, exec migrationExecutor, id string) (InstanceOwnerTransfer, error) {
	var transfer InstanceOwnerTransfer
	var state, createdAt, expiresAt string
	var completedAt sql.NullString
	err := exec.QueryRowContext(ctx, `SELECT id,from_user_id,to_user_id,state,created_at,expires_at,completed_at FROM instance_owner_transfers WHERE id=?`, id).Scan(&transfer.ID, &transfer.FromUserID, &transfer.ToUserID, &state, &createdAt, &expiresAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceOwnerTransfer{}, ErrOwnerTransferNotFound
	}
	if err != nil {
		return InstanceOwnerTransfer{}, fmt.Errorf("read instance owner transfer: %w", err)
	}
	transfer.State = OwnerTransferState(state)
	var parseErr error
	if transfer.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
		return InstanceOwnerTransfer{}, fmt.Errorf("parse owner transfer creation time: %w", parseErr)
	}
	if transfer.ExpiresAt, parseErr = time.Parse(time.RFC3339Nano, expiresAt); parseErr != nil {
		return InstanceOwnerTransfer{}, fmt.Errorf("parse owner transfer expiry time: %w", parseErr)
	}
	if completedAt.Valid {
		completed, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			return InstanceOwnerTransfer{}, fmt.Errorf("parse owner transfer completion time: %w", err)
		}
		transfer.CompletedAt = &completed
	}
	return transfer, nil
}

func requireOwnerAccount(ctx context.Context, exec migrationExecutor, accountID string) error {
	var exists int
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, accountID).Scan(&exists); err != nil {
		return fmt.Errorf("check owner account: %w", err)
	}
	if exists != 1 {
		return ErrOwnerAccountNotFound
	}
	return nil
}

func setOwnerTransferTerminal(ctx context.Context, exec migrationExecutor, id string, state OwnerTransferState, now time.Time) error {
	result, err := exec.ExecContext(ctx, `UPDATE instance_owner_transfers SET state=?,completed_at=? WHERE id=? AND state='pending'`, string(state), formatOwnerTime(now), id)
	if err != nil {
		return fmt.Errorf("finish owner transfer: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("verify owner transfer state: %w", err)
	}
	if changed != 1 {
		return ErrOwnerTransferNotPending
	}
	return nil
}

func insertInstanceOwnerAudit(ctx context.Context, exec migrationExecutor, actorID, action, resourceType, resourceID string, status int, at time.Time) error {
	var actor any
	if actorID != "" {
		actor = actorID
	}
	if _, err := exec.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,?,?,?,?,?,?)`, newID(), actor, action, resourceType, resourceID, status, formatOwnerTime(at)); err != nil {
		return fmt.Errorf("record instance owner audit event: %w", err)
	}
	return nil
}

// Use fixed-width UTC precision so persisted owner times also sort safely as text.
func formatOwnerTime(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func withInstanceOwnerWriteTx(ctx context.Context, db *sql.DB, apply func(migrationExecutor) error) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire instance owner connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin instance owner transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(rollbackCtx, `ROLLBACK`); rollbackErr != nil && err == nil {
			err = fmt.Errorf("rollback instance owner transaction: %w", rollbackErr)
		}
	}()
	if err := apply(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit instance owner transaction: %w", err)
	}
	committed = true
	return nil
}
