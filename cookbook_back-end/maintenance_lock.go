package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrMaintenanceLockHeld = errors.New("another process holds the cookbook maintenance lock")

type maintenanceLockMode int

const (
	maintenanceLockShared maintenanceLockMode = iota + 1
	maintenanceLockExclusive
)

type maintenanceLock struct {
	file *os.File
}

func maintenanceLockPath(databasePath string) (string, error) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" || databasePath == ":memory:" || strings.HasPrefix(databasePath, "file:") {
		return "", errors.New("maintenance locking requires a filesystem database path")
	}
	abs, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("resolve database path for maintenance lock: %w", err)
	}
	return abs + ".maintenance.lock", nil
}

func acquireMaintenanceLock(databasePath string, mode maintenanceLockMode) (*maintenanceLock, error) {
	path, err := maintenanceLockPath(databasePath)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open maintenance lock file: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect maintenance lock file: %w", err)
	}
	if err := lockMaintenanceFile(file, mode); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &maintenanceLock{file: file}, nil
}

func (lock *maintenanceLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockMaintenanceFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
