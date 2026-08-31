package database

import (
	"io/fs"

	"github.com/joshuanmoses/piCloud/backend/migrations"
)

// realMigrations returns the migrations actually embedded in the binary.
func realMigrations() fs.FS { return migrations.FS }
