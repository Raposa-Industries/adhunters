// Package migrations holds the tracks schema's migrations, embedded in
// tracks-loader, which applies them at its deploy (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Tracks owns.
const Schema = "tracks"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
