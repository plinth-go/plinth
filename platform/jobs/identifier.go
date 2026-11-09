// Package jobs provides the optional PostgreSQL-backed durable job queue,
// worker, and scheduler. Applications that do not import this package do not
// compile pgx-backed job support from Plinth.
package jobs

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const maxIdentifierBytes = 255

func validateIdentifier(value, label string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", label)
	}
	if len(value) > maxIdentifierBytes {
		return fmt.Errorf("%s must not exceed %d bytes", label, maxIdentifierBytes)
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return fmt.Errorf("%s must contain only printable characters", label)
		}
	}
	return nil
}
