// Package migrations holds Create's migrations (schemas create_app and
// create_api), embedded in the create binary, which applies them when it
// starts (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema Create owns. "create" is a reserved word in SQL.
const Schema = "create_app"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
