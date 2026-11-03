package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type sqliteExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// withImmediateSQLiteWriteTx pins a connection and serializes the callback
// behind SQLite's immediate writer lock.
func withImmediateSQLiteWriteTx(ctx context.Context, db *sql.DB, apply func(sqliteExecutor) error) (err error) {
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
