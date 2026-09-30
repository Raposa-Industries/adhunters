// Package migrations holds the intel schema's migrations, embedded in
// intel-numbers, which applies them at its deploy (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Intel owns.
const Schema = "intel"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
