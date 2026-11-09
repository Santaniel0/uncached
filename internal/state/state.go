// Package state stores what uncached learned about each test after a run,
// so the next run can say what changed.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/testlog"
)

// Schema is the version of the file format written by this package.
const Schema = 1

var (
	// ErrNoState means there is no state file yet.
	ErrNoState = errors.New("no saved state")
	// ErrSchema means the file was written by an incompatible version.
	ErrSchema = errors.New("unsupported state schema")
)

// State is everything recorded after a run.
type State struct {
	Schema  int       `json:"schema"`
	SavedAt time.Time `json:"saved_at"`
	// Commit is the git HEAD the run tested, if known.
	Commit string                         `json:"commit,omitempty"`
	Tests  map[ids.ImportPath]*TestRecord `json:"tests"`
	// Files holds the latest known file digests of local packages.
	Files fingerprint.Files `json:"files,omitempty"`
}

// New returns an empty state.
func New() *State {
	return &State{Schema: Schema, Tests: map[ids.ImportPath]*TestRecord{}, Files: fingerprint.Files{}}
}

// TestRecord is what is known about one package's test after a run.
type TestRecord struct {
	Dir  string `json:"dir"`
	Root string `json:"root"`
	// TestIDs are the test IDs under which cmd/go stored the result. There
	// can be two: one derived from the link inputs and one from the binary.
	TestIDs []ids.ActionID `json:"test_ids,omitempty"`
	// InputID is the hash cmd/go computed over the inputs.
	InputID ids.ActionID `json:"input_id,omitzero"`
	// Inputs is the state of each input when the result was stored. Nil
	// when the input list could not be read.
	Inputs []inputs.Observation `json:"inputs,omitempty"`
	Binary fingerprint.Test     `json:"binary"`
	Config fingerprint.Config   `json:"config,omitempty"`
	// NoResult is set when the test ran but nothing was stored in GOCACHE
	// for it (it failed, or caching was disabled) and no earlier result
	// was recorded either.
	NoResult   bool      `json:"no_result,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// HasTestID reports whether any of want was recorded for this test.
func (r *TestRecord) HasTestID(want []ids.ActionID) bool {
	for _, id := range want {
		if slices.Contains(r.TestIDs, id) {
			return true
		}
	}
	return false
}

// Side returns the record's fingerprint for comparison, using files for
// per-file detail.
func (r *TestRecord) Side(files fingerprint.Files) fingerprint.Side {
	return fingerprint.Side{Test: r.Binary, Config: r.Config, Files: files}
}

// Inputs returns the distinct inputs recorded across all tests.
func (s *State) Inputs() []testlog.Input {
	seen := map[testlog.Input]bool{}
	var out []testlog.Input
	for _, p := range slices.Sorted(maps.Keys(s.Tests)) {
		for _, obs := range s.Tests[p].Inputs {
			if !seen[obs.Input] {
				seen[obs.Input] = true
				out = append(out, obs.Input)
			}
		}
	}
	return out
}

// Observations returns every recorded observation, for reuse of digests.
func (s *State) Observations() []inputs.Observation {
	var out []inputs.Observation
	for _, r := range s.Tests {
		out = append(out, r.Inputs...)
	}
	return out
}

// Load reads a state file. A missing file yields ErrNoState.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrNoState, path)
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var probe struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", path, err)
	}
	if probe.Schema != Schema {
		return nil, fmt.Errorf("%w: %s has version %d, want %d", ErrSchema, path, probe.Schema, Schema)
	}
	s := New()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", path, err)
	}
	if s.Tests == nil {
		s.Tests = map[ids.ImportPath]*TestRecord{}
	}
	if s.Files == nil {
		s.Files = fingerprint.Files{}
	}
	return s, nil
}

// Save writes s to path atomically, creating the directory if needed.
func Save(path string, s *State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to a temporary file next to path and renames
// it into place, so readers never see a half-written file.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// DefaultPath is where state lives unless the user says otherwise: inside
// GOCACHE, so CI setups that already persist the build cache keep it too.
// cmd/go only trims and cleans the 00..ff subdirectories, so a sibling
// directory survives both. The file name is keyed by the module (or the
// working directory outside a module).
func DefaultPath(gocache, gomod, dir string) string {
	key := gomod
	if key == "" || key == os.DevNull {
		key = dir
	}
	sum := sha256.Sum256([]byte(filepath.Clean(key)))
	return filepath.Join(gocache, "uncached", "state-"+hex.EncodeToString(sum[:8])+".json")
}

// ReportPath is where the report of the last run is kept, next to the
// state file.
func ReportPath(statePath string) string {
	return strings.TrimSuffix(statePath, ".json") + ".report.json"
}
