// Package migrations holds the launch schema's migrations, embedded in
// launch-web, which applies them when it starts (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Launch owns.
const Schema = "launch"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
