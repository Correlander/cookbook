//go:build !linux

package main

import (
	"errors"
	"os"
)

func lockMaintenanceFile(_ *os.File, _ maintenanceLockMode) error {
	return errors.New("maintenance locking is supported only on Linux")
}

func unlockMaintenanceFile(_ *os.File) error {
	return errors.New("maintenance locking is supported only on Linux")
}
