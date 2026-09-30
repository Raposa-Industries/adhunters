// Package migrations holds the funnels schema's migrations, embedded in
// funnels-loader, which applies them with `funnels-loader migrate`
// (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Funnels owns.
const Schema = "funnels"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
