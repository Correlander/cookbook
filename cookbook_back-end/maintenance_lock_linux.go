//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockMaintenanceFile(file *os.File, mode maintenanceLockMode) error {
	operation := syscall.LOCK_NB
	switch mode {
	case maintenanceLockShared:
		operation |= syscall.LOCK_SH
	case maintenanceLockExclusive:
		operation |= syscall.LOCK_EX
	default:
		return errors.New("invalid maintenance lock mode")
	}
	if mode == maintenanceLockShared {
		operation &^= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), operation); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrMaintenanceLockHeld
		}
		return fmt.Errorf("acquire maintenance lock: %w", err)
	}
	return nil
}

func unlockMaintenanceFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("release maintenance lock: %w", err)
	}
	return nil
}
