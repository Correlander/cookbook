package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const instanceOwnerCLIUsage = `Usage:
  cookbook-backend instance owner claim --account-id <id>
  cookbook-backend instance owner recover --account-id <id> --reason <reason>

Both commands require an interactive confirmation and stop while the server is running.`

func runInstanceOwnerCLI(args []string, input io.Reader, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "instance" {
		return false, nil
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
