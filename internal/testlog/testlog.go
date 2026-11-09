// Package testlog parses the input log a Go test binary writes for the test
// cache and resolves it into the inputs the go command actually hashes.
//
// The format is produced by src/testing/internal/testdeps/deps.go and
// consumed by computeTestInputsID in src/cmd/go/internal/test/test.go
// (checked against go1.27.3): a "# test log" header followed by one
// "<op> <name>" line per getenv, open, stat or chdir call.
package testlog

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Op is the kind of access a test made.
type Op uint8

const (
	OpGetenv Op = iota + 1
	OpOpen
	OpStat
	OpChdir
)

var opNames = [...]string{OpGetenv: "getenv", OpOpen: "open", OpStat: "stat", OpChdir: "chdir"}

func (o Op) valid() bool { return o >= OpGetenv && o <= OpChdir }

func (o Op) String() string {
	if o.valid() {
		return opNames[o]
	}
	return fmt.Sprintf("Op(%d)", uint8(o))
}

// ErrUnknownOp is returned for operation names this package does not know.
var ErrUnknownOp = errors.New("unknown test log operation")

// ParseOp parses an operation name as it appears in the log.
func ParseOp(s string) (Op, error) {
	for o := OpGetenv; o <= OpChdir; o++ {
		if opNames[o] == s {
			return o, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownOp, s)
}

// MarshalText implements encoding.TextMarshaler.
func (o Op) MarshalText() ([]byte, error) {
	if !o.valid() {
		return nil, fmt.Errorf("%w: %d", ErrUnknownOp, uint8(o))
	}
	return []byte(o.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (o *Op) UnmarshalText(b []byte) error {
	v, err := ParseOp(string(b))
	if err != nil {
		return err
	}
	*o = v
	return nil
}

// ErrMalformed is returned for logs the go command would also reject.
var ErrMalformed = errors.New("malformed test log")

const header = "# test log\n"

// Entry is one raw line of the log.
type Entry struct {
	Op   Op
	Name string
}

// Parse parses a test log as stored in the build cache.
func Parse(data []byte) ([]Entry, error) {
	rest, ok := bytes.CutPrefix(data, []byte(header))
	if !ok {
		return nil, fmt.Errorf("%w: missing %q header", ErrMalformed, strings.TrimSpace(header))
	}
	var out []Entry
	for i, line := range strings.Split(string(rest), "\n") {
		if line == "" {
			continue
		}
		opName, name, found := strings.Cut(line, " ")
		if !found {
			return nil, fmt.Errorf("%w: line %d: %q", ErrMalformed, i+2, line)
		}
		op, err := ParseOp(opName)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformed, i+2, err)
		}
		out = append(out, Entry{Op: op, Name: name})
	}
	return out, nil
}

// Input is something the go command hashes into a test's cache key: an
// environment variable name, or a cleaned absolute path.
type Input struct {
	Op   Op     `json:"op"`
	Name string `json:"name"`
}

func (in Input) String() string { return in.Op.String() + " " + in.Name }

// IsFile reports whether the input names a path rather than a variable.
func (in Input) IsFile() bool { return in.Op != OpGetenv }

// GODEBUG is hashed for every test even though the log never mentions it,
// because the runtime reads it without going through os.Getenv.
var GODEBUG = Input{Op: OpGetenv, Name: "GODEBUG"}

// Resolve turns log entries into the inputs cmd/go hashes for a package in
// pkgDir whose module (or GOPATH/GOROOT) root is root. Like cmd/go it
// resolves relative paths against the current directory recorded by chdir
// entries and drops files outside root, which it never rechecks. The
// result starts with GODEBUG and has no duplicates.
func Resolve(entries []Entry, pkgDir, root string) []Input {
	out := []Input{GODEBUG}
	seen := map[Input]bool{GODEBUG: true}
	add := func(in Input) {
		if !seen[in] {
			seen[in] = true
			out = append(out, in)
		}
	}
	pwd := pkgDir
	for _, e := range entries {
		switch e.Op {
		case OpGetenv:
			add(Input{Op: OpGetenv, Name: e.Name})
		case OpChdir:
			pwd = e.Name
			add(Input{Op: OpChdir, Name: filepath.Clean(e.Name)})
		case OpOpen, OpStat:
			name := e.Name
			if !filepath.IsAbs(name) {
				name = filepath.Join(pwd, name)
			}
			name = filepath.Clean(name)
			if root == "" || !Within(root, name) {
				continue
			}
			add(Input{Op: e.Op, Name: name})
		}
	}
	return out
}

// Within reports whether path is root or lies below it. On Windows the
// comparison ignores case, as filepath.Rel does there.
func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
