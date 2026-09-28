// Package migrations holds the raposa schema's migrations, embedded in
// raposa-engine, which applies them when it starts (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Raposa owns.
const Schema = "raposa"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
