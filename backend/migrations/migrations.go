// Package migrations embeds the SQL schema migrations so that the application
// binary carries its own schema. There is no separate migration tool to install
// and no migration container that can fail independently of the app.
package migrations

import "embed"

// FS holds every .sql migration, named NNNN_description.sql and applied in
// ascending numeric order.
//
//go:embed *.sql
var FS embed.FS
