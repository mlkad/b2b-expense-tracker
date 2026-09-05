// Package db embeds the migrations into the binary.
//
// Embedding rather than shipping the directory alongside is what makes an
// image unable to drift from the schema it expects: the binary and the
// migrations are one artefact, so there is no arrangement in which a container
// runs new code against a directory of old SQL.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
