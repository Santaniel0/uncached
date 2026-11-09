// Package ids holds the typed identifiers shared across uncached.
package ids

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ImportPath is a package import path as the go command prints it. Test
// variants keep their suffix, e.g. "example.com/p [example.com/p.test]".
type ImportPath string

// Base returns the import path without a test-variant suffix.
func (p ImportPath) Base() ImportPath {
	if i := strings.Index(string(p), " ["); i >= 0 {
		return p[:i]
	}
	return p
}

// ModulePath is a module path such as "golang.org/x/mod".
type ModulePath string

// ActionID is a key in the go command's build cache: a salted SHA-256.
type ActionID [32]byte

// ErrBadActionID is returned for strings that are not 64 hex digits.
var ErrBadActionID = errors.New("malformed action ID")

// ParseActionID parses the hex form printed by the go command.
func ParseActionID(s string) (ActionID, error) {
	var id ActionID
	if len(s) != hex.EncodedLen(len(id)) {
		return ActionID{}, fmt.Errorf("%w: %q", ErrBadActionID, s)
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return ActionID{}, fmt.Errorf("%w: %q", ErrBadActionID, s)
	}
	return id, nil
}

func (id ActionID) String() string { return hex.EncodeToString(id[:]) }

// IsZero reports whether id is unset.
func (id ActionID) IsZero() bool { return id == ActionID{} }

// MarshalText implements encoding.TextMarshaler.
func (id ActionID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ActionID) UnmarshalText(b []byte) error {
	v, err := ParseActionID(string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}
