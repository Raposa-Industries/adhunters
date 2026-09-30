// Package migrations holds the library schema's migrations, embedded in the
// library binary, which applies them when it starts (kit/migrate).
package migrations

import (
	"embed"

	"github.com/Raposa-Industries/adhunters/kit/migrate"
)

//go:embed sql/*.sql
var files embed.FS

// Schema is the schema the library owns.
const Schema = "library"

// Load returns the migrations in order.
func Load() ([]migrate.Migration, error) { return migrate.Load(files, "sql") }
