package main

import (
	"context"
	"fmt"
)

const (
	anonymousHomePresetLibraryFirst   = "library_first"
	anonymousHomePresetDiscoveryFirst = "discovery_first"
	defaultAnonymousHomePreset        = anonymousHomePresetLibraryFirst
)

const anonymousHomePresetSchemaDDL = `ALTER TABLE instance_settings
ADD COLUMN anonymous_home_preset TEXT NOT NULL DEFAULT 'library_first'
CHECK (anonymous_home_preset IN ('library_first','discovery_first'))`

func applyAnonymousHomePresetMigration(ctx context.Context, exec migrationExecutor) error {
	if _, err := exec.ExecContext(ctx, anonymousHomePresetSchemaDDL); err != nil {
		return fmt.Errorf("add anonymous homepage preset setting: %w", err)
	}
	return nil
}

func validAnonymousHomePreset(value string) bool {
	return value == anonymousHomePresetLibraryFirst || value == anonymousHomePresetDiscoveryFirst
}
