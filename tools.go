//go:build tools

// Package tools pins code-generation tooling (gqlgen) as an explicit dependency
// so `go mod tidy` keeps it and stubs can be regenerated with `go generate`.
package tools

import (
	_ "github.com/99designs/gqlgen"
)
