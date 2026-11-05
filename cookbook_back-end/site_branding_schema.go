package main

import (
	"context"
)

const (
	defaultSiteName = "Cookbook"
	defaultPalette  = "garden"
)

const siteNameColumnDDL = `ALTER TABLE instance_settings ADD COLUMN site_name TEXT NOT NULL DEFAULT 'Cookbook'`
const paletteColumnDDL = `ALTER TABLE instance_settings ADD COLUMN palette TEXT NOT NULL DEFAULT 'garden' CHECK(palette IN ('garden','plum'))`
const siteBrandingSchemaDDL = siteNameColumnDDL + "\x00" + paletteColumnDDL

func applySiteBrandingMigration(ctx context.Context, exec migrationExecutor) error {
	for _, statement := range []string{siteNameColumnDDL, paletteColumnDDL} {
		if _, err := exec.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func validPalette(value string) bool {
	return value == defaultPalette || value == "plum"
}
