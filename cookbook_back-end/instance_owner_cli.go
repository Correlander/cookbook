package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const instanceOwnerCLIUsage = `Usage:
  cookbook-backend instance account lookup --email <address>
  cookbook-backend instance owner claim --account-id <id>
  cookbook-backend instance owner recover --account-id <id> --reason <reason>
  cookbook-backend instance passkey-enrollment issue --account-id <id>
  cookbook-backend instance passkey-enrollment revoke --account-id <id>

The server must be stopped for all commands. Mutating owner and passkey commands
also require an interactive confirmation; account lookup is read-only.`

func runInstanceOwnerCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "instance" {
		return false, nil
	}
	if len(args) > 1 && args[1] == "account" {
		return runInstanceAccountCLI(args[2:], output)
	}
	if len(args) > 1 && args[1] == "passkey-enrollment" {
		return runPasskeyEnrollmentCLI(args[2:], input, output)
	}
	if len(args) == 1 || args[1] != "owner" {
		_, _ = fmt.Fprintln(output, instanceOwnerCLIUsage)
		return true, errors.New("unsupported instance command")
	}
	if len(args) == 3 && args[2] == "--help" {
		_, _ = fmt.Fprintln(output, instanceOwnerCLIUsage)
		return true, nil
	}
	if len(args) < 3 || (args[2] != "claim" && args[2] != "recover") {
		_, _ = fmt.Fprintln(output, instanceOwnerCLIUsage)
		return true, errors.New("expected owner claim or recover")
	}
	command := args[2]
	accountID, reason, err := parseOwnerCLIFlags(args[3:], command == "recover")
	if err != nil {
		return true, err
	}
	if input == os.Stdin {
		info, statErr := os.Stdin.Stat()
		if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
			return true, errors.New("owner claim and recovery require an interactive terminal")
		}
	}
	confirmation := strings.ToUpper(command) + " " + accountID
	if !confirmOwnerOperation(input, output, confirmation) {
		return true, errors.New("owner operation was not confirmed")
	}

	cfg, err := loadConfig()
	if err != nil {
		return true, err
	}
	lock, err := acquireMaintenanceLock(cfg.database, maintenanceLockExclusive)
	if err != nil {
		return true, fmt.Errorf("offline owner operation requires exclusive maintenance lock: %w", err)
	}
	defer lock.Close()
	db, err := openDatabase(cfg.database)
	if err != nil {
		return true, err
	}
	defer db.Close()

	var authority InstanceAuthority
	if command == "claim" {
		authority, err = ClaimInstanceOwner(context.Background(), db, accountID)
	} else {
		authority, err = RecoverInstanceOwner(context.Background(), db, accountID, reason)
	}
	if err != nil {
		return true, ownerCLIError(err)
	}
	_, err = fmt.Fprintf(output, "Instance owner is now %s (generation %d).\n", authority.OwnerUserID, authority.Generation)
	return true, err
}

func runPasskeyEnrollmentCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 1 && args[0] == "--help" {
		_, _ = fmt.Fprintln(output, instanceOwnerCLIUsage)
		return true, nil
	}
	if len(args) < 1 || (args[0] != "issue" && args[0] != "revoke") {
		return true, errors.New("expected passkey-enrollment issue or revoke")
	}
	command := args[0]
	accountID, _, err := parseOwnerCLIFlags(args[1:], false)
	if err != nil {
		return true, err
	}
	if input == os.Stdin {
		info, statErr := os.Stdin.Stat()
		if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
			return true, errors.New("passkey enrollment requires an interactive terminal")
		}
	}
	if output == os.Stdout {
		info, statErr := os.Stdout.Stat()
		if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
			return true, errors.New("passkey enrollment token requires an interactive terminal")
		}
	}
	confirmation := strings.ToUpper(command) + " PASSKEY ENROLLMENT " + accountID
	_, _ = fmt.Fprintf(output, "This %s an offline first-passkey grant for account %s. Type %q to continue: ", command, accountID, confirmation)
	line, readErr := bufio.NewReader(input).ReadString('\n')
	if readErr != nil || strings.TrimSpace(line) != confirmation {
		return true, errors.New("passkey enrollment operation was not confirmed")
	}
	cfg, err := loadConfig()
	if err != nil {
		return true, err
	}
	lock, err := acquireMaintenanceLock(cfg.database, maintenanceLockExclusive)
	if err != nil {
		return true, fmt.Errorf("offline passkey enrollment requires exclusive maintenance lock: %w", err)
	}
	defer lock.Close()
	db, err := openDatabase(cfg.database)
	if err != nil {
		return true, err
	}
	defer db.Close()
	var token string
	err = withInstanceOwnerWriteTx(context.Background(), db, func(exec migrationExecutor) error {
		var found int
		if err := exec.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM users WHERE id=?`, accountID).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return ErrOwnerAccountNotFound
		}
		if command == "issue" {
			if err := exec.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM webauthn_credentials WHERE user_id=?`, accountID).Scan(&found); err != nil {
				return err
			}
			if found != 0 {
				return errors.New("account already has a passkey")
			}
			id, raw, digest, err := newEnrollmentToken()
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			issued := now.Format(time.RFC3339Nano)
			expires := now.Add(15 * time.Minute).Format(time.RFC3339Nano)
			if _, err := exec.ExecContext(context.Background(), `UPDATE passkey_enrollment_grants SET revoked_at=? WHERE user_id=? AND purpose='initial_passkey_registration' AND consumed_at IS NULL AND revoked_at IS NULL`, issued, accountID); err != nil {
				return err
			}
			if _, err := exec.ExecContext(context.Background(), `DELETE FROM webauthn_ceremonies WHERE purpose='initial_registration' AND user_id=?`, accountID); err != nil {
				return err
			}
			if _, err := exec.ExecContext(context.Background(), `INSERT INTO passkey_enrollment_grants(id,token_hash,user_id,purpose,issued_at,expires_at) VALUES(?,?,?,'initial_passkey_registration',?,?)`, id, digest, accountID, issued, expires); err != nil {
				return err
			}
			if _, err := exec.ExecContext(context.Background(), `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,NULL,'passkey_enrollment.issue','auth',?,200,?)`, newID(), accountID, issued); err != nil {
				return err
			}
			token = raw
			return nil
		}
		now := timestamp()
		result, err := exec.ExecContext(context.Background(), `UPDATE passkey_enrollment_grants SET revoked_at=? WHERE user_id=? AND purpose='initial_passkey_registration' AND consumed_at IS NULL AND revoked_at IS NULL`, now, accountID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("no active enrollment grant exists")
		}
		if _, err := exec.ExecContext(context.Background(), `DELETE FROM webauthn_ceremonies WHERE purpose='initial_registration' AND user_id=?`, accountID); err != nil {
			return err
		}
		_, err = exec.ExecContext(context.Background(), `INSERT INTO audit_events(id,actor_id,action,resource_type,resource_id,response_status,created_at) VALUES(?,NULL,'passkey_enrollment.revoke','auth',?,200,?)`, newID(), accountID, now)
		return err
	})
	if err != nil {
		return true, ownerCLIError(err)
	}
	if command == "issue" {
		if _, err := fmt.Fprintf(output, "Passkey enrollment token (expires in 15 minutes):\n%s\n", token); err != nil {
			_, _ = db.Exec(`UPDATE passkey_enrollment_grants SET revoked_at=? WHERE token_hash=? AND consumed_at IS NULL AND revoked_at IS NULL`, timestamp(), sha256Bytes([]byte(strings.SplitN(token, ".", 2)[1])))
			return true, err
		}
	} else if _, err := fmt.Fprintf(output, "Active passkey enrollment grant revoked for %s.\n", accountID); err != nil {
		return true, err
	}
	return true, nil
}

func sha256Bytes(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }

func parseOwnerCLIFlags(args []string, needsReason bool) (accountID, reason string, err error) {
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return "", "", fmt.Errorf("%s requires a value", args[i])
		}
		value := args[i+1]
		switch args[i] {
		case "--account-id":
			if accountID != "" || strings.TrimSpace(value) == "" {
				return "", "", errors.New("exactly one nonempty --account-id is required")
			}
			accountID = strings.TrimSpace(value)
		case "--reason":
			if reason != "" || strings.TrimSpace(value) == "" {
				return "", "", errors.New("exactly one nonempty --reason is required")
			}
			reason = strings.TrimSpace(value)
		default:
			return "", "", fmt.Errorf("unknown owner option %q", args[i])
		}
		i++
	}
	if accountID == "" {
		return "", "", errors.New("explicit --account-id is required")
	}
	if needsReason && reason == "" {
		return "", "", errors.New("recovery requires --reason")
	}
	if !needsReason && reason != "" {
		return "", "", errors.New("--reason is only valid for recovery")
	}
	if len(reason) > 500 {
		return "", "", errors.New("--reason must not exceed 500 bytes")
	}
	return accountID, reason, nil
}

func confirmOwnerOperation(input io.Reader, output io.Writer, expected string) bool {
	_, _ = fmt.Fprintf(output, "This changes instance authority. Type %q to continue: ", expected)
	line, err := bufio.NewReader(input).ReadString('\n')
	return err == nil && strings.TrimSpace(line) == expected
}

func ownerCLIError(err error) error {
	switch {
	case errors.Is(err, ErrMaintenanceLockHeld):
		return err
	case errors.Is(err, ErrOwnerAccountNotFound):
		return errors.New("the specified account does not exist")
	case errors.Is(err, ErrInstanceOwnerAlreadySet):
		return errors.New("instance owner is already claimed")
	default:
		return err
	}
}
