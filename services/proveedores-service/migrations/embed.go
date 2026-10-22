// Package migrations embebe los scripts SQL para que el binario los aplique al arrancar.
package migrations

import "embed"

// FS contiene los archivos NNN_*.sql, que se aplican en orden lexicográfico.
//
//go:embed *.sql
var FS embed.FS
