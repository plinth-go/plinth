// Package migrations embeds the SQL schema used by the optional jobs package.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

func FS() fs.FS { return files }
