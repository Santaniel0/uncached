package fingerprint

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/ids"
)

// Config holds the settings that feed a test binary's ID: build
// environment variables by name and relevant go test flags as "-name".
type Config map[string]string

// NewConfig combines go env settings with go test flags.
func NewConfig(env GoEnv, flags []gotest.Flag) Config {
	c := Config(env.settings())
	for _, f := range flags {
		c["-"+f.Name] = f.Value
	}
	return c
}

// Test is the fingerprint of one test binary.
type Test struct {
	// Sources maps each local package linked into the binary, test
	// variants included, to a digest of its files.
	Sources map[ids.ImportPath]string `json:"sources,omitempty"`
	// Modules maps each module dependency to its version.
	Modules map[ids.ModulePath]string `json:"modules,omitempty"`
}

// Files maps a local package to its file digests, keyed by file name
// relative to the package directory.
type Files map[ids.ImportPath]map[string]string

// Build fingerprints the test binaries of the tested packages. Packages
// without a test binary in listed (no test files, or load errors) are
// skipped.
func Build(ctx context.Context, listed map[ids.ImportPath]Package, tested []ids.ImportPath, workers int) (map[ids.ImportPath]Test, Files, error) {
	tests := make(map[ids.ImportPath]Test)
	local := make(map[ids.ImportPath]Package)
	for _, p := range tested {
		main, ok := listed[TestMain(p)]
		if !ok {
			continue
		}
		t := Test{Sources: map[ids.ImportPath]string{}, Modules: map[ids.ModulePath]string{}}
		for _, d := range main.Deps {
			dep, ok := listed[d]
			switch {
			case !ok || dep.Standard:
			case dep.local():
				t.Sources[d] = ""
				local[d] = dep
			case dep.Module != nil:
				path, version := dep.moduleVersion()
				t.Modules[path] = version
			}
		}
		tests[p] = t
	}
	files, err := hashPackages(ctx, local, workers)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tests {
		for p := range t.Sources {
			t.Sources[p] = packageDigest(files[p])
		}
	}
	return tests, files, nil
}

type fileJob struct {
	pkg  ids.ImportPath
	name string
	path string
}

func hashPackages(ctx context.Context, pkgs map[ids.ImportPath]Package, workers int) (Files, error) {
	var jobs []fileJob
	for p, pkg := range pkgs {
		for _, name := range pkg.sourceFiles() {
			jobs = append(jobs, fileJob{pkg: p, name: name, path: filepath.Join(pkg.Dir, filepath.FromSlash(name))})
		}
	}
	// Test variants share files with their base package; hash each path once.
	unique := make(map[string]string)
	for _, j := range jobs {
		unique[j.path] = ""
	}
	paths := slices.Collect(maps.Keys(unique))
	digests, err := hashFiles(ctx, paths, workers)
	if err != nil {
		return nil, err
	}
	files := make(Files, len(pkgs))
	for p := range pkgs {
		files[p] = map[string]string{}
	}
	for _, j := range jobs {
		files[j.pkg][j.name] = digests[j.path]
	}
	return files, nil
}

func hashFiles(ctx context.Context, paths []string, workers int) (map[string]string, error) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	out := make(map[string]string, len(paths))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	ch := make(chan string)
	for range min(workers, max(len(paths), 1)) {
		wg.Go(func() {
			for p := range ch {
				d, err := hashFile(p)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				out[p] = d
				mu.Unlock()
			}
		})
	}
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		ch <- p
	}
	close(ch)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, firstErr
}

// missingFile stands in for the digest of a file go list named but that is
// gone by the time it is hashed.
const missingFile = "missing"

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return missingFile, nil
	}
	if err != nil {
		return "", fmt.Errorf("hash source file: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash source file %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func packageDigest(files map[string]string) string {
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s\x00%s\n", name, files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DiffKind says what part of a test binary's fingerprint changed.
type DiffKind uint8

const (
	DiffPackageChanged DiffKind = iota + 1
	DiffPackageAdded
	DiffPackageRemoved
	DiffModuleChanged
	DiffModuleAdded
	DiffModuleRemoved
	DiffSettingChanged
)

var diffNames = [...]string{
	DiffPackageChanged: "package-changed",
	DiffPackageAdded:   "package-added",
	DiffPackageRemoved: "package-removed",
	DiffModuleChanged:  "module-changed",
	DiffModuleAdded:    "module-added",
	DiffModuleRemoved:  "module-removed",
	DiffSettingChanged: "setting-changed",
}

func (k DiffKind) String() string {
	if k >= DiffPackageChanged && int(k) < len(diffNames) {
		return diffNames[k]
	}
	return fmt.Sprintf("DiffKind(%d)", uint8(k))
}

// ErrUnknownKind is returned when decoding an unknown enum name.
var ErrUnknownKind = errors.New("unknown kind")

// MarshalText implements encoding.TextMarshaler.
func (k DiffKind) MarshalText() ([]byte, error) { return marshalName(diffNames[:], int(k)) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *DiffKind) UnmarshalText(b []byte) error {
	i, err := unmarshalName(diffNames[:], b)
	*k = DiffKind(i)
	return err
}

// FileChangeKind says how one source file changed.
type FileChangeKind uint8

const (
	FileModified FileChangeKind = iota + 1
	FileAdded
	FileRemoved
)

var fileChangeNames = [...]string{FileModified: "modified", FileAdded: "added", FileRemoved: "removed"}

func (k FileChangeKind) String() string {
	if k >= FileModified && int(k) < len(fileChangeNames) {
		return fileChangeNames[k]
	}
	return fmt.Sprintf("FileChangeKind(%d)", uint8(k))
}

// MarshalText implements encoding.TextMarshaler.
func (k FileChangeKind) MarshalText() ([]byte, error) { return marshalName(fileChangeNames[:], int(k)) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *FileChangeKind) UnmarshalText(b []byte) error {
	i, err := unmarshalName(fileChangeNames[:], b)
	*k = FileChangeKind(i)
	return err
}

func marshalName(names []string, i int) ([]byte, error) {
	if i <= 0 || i >= len(names) {
		return nil, fmt.Errorf("%w: %d", ErrUnknownKind, i)
	}
	return []byte(names[i]), nil
}

func unmarshalName(names []string, b []byte) (int, error) {
	for i, n := range names {
		if i > 0 && n == string(b) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownKind, b)
}

// FileChange is one changed source file.
type FileChange struct {
	Name string         `json:"name"`
	Kind FileChangeKind `json:"kind"`
}

// Diff is one difference between two fingerprints.
type Diff struct {
	Kind DiffKind `json:"kind"`
	// Subject is an import path, a module path, or a setting name.
	Subject string       `json:"subject"`
	Old     string       `json:"old,omitempty"`
	New     string       `json:"new,omitempty"`
	Files   []FileChange `json:"files,omitempty"`
}

// Side is one of the two fingerprints being compared, with the context
// needed to explain it.
type Side struct {
	Test   Test
	Config Config
	// Files are the file digests recorded alongside Test. They are used for
	// per-file detail only when they match the package digest in Test.
	Files Files
}

// Compare lists what differs between two fingerprints of the same test.
func Compare(old, cur Side) []Diff {
	var out []Diff
	for _, p := range unionKeys(old.Test.Sources, cur.Test.Sources) {
		o, inOld := old.Test.Sources[p]
		c, inCur := cur.Test.Sources[p]
		switch {
		case !inOld:
			out = append(out, Diff{Kind: DiffPackageAdded, Subject: string(p)})
		case !inCur:
			out = append(out, Diff{Kind: DiffPackageRemoved, Subject: string(p)})
		case o != c:
			out = append(out, Diff{Kind: DiffPackageChanged, Subject: string(p), Files: fileChanges(p, old, cur)})
		}
	}
	for _, m := range unionKeys(old.Test.Modules, cur.Test.Modules) {
		o, inOld := old.Test.Modules[m]
		c, inCur := cur.Test.Modules[m]
		switch {
		case !inOld:
			out = append(out, Diff{Kind: DiffModuleAdded, Subject: string(m), New: c})
		case !inCur:
			out = append(out, Diff{Kind: DiffModuleRemoved, Subject: string(m), Old: o})
		case o != c:
			out = append(out, Diff{Kind: DiffModuleChanged, Subject: string(m), Old: o, New: c})
		}
	}
	for _, k := range unionKeys(old.Config, cur.Config) {
		if old.Config[k] != cur.Config[k] {
			out = append(out, Diff{Kind: DiffSettingChanged, Subject: k, Old: old.Config[k], New: cur.Config[k]})
		}
	}
	slices.SortStableFunc(out, func(a, b Diff) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Subject, b.Subject))
	})
	return out
}

func fileChanges(p ids.ImportPath, old, cur Side) []FileChange {
	of, okOld := old.Files[p]
	cf, okCur := cur.Files[p]
	if !okOld || !okCur || packageDigest(of) != old.Test.Sources[p] || packageDigest(cf) != cur.Test.Sources[p] {
		return nil
	}
	var out []FileChange
	for _, name := range unionKeys(of, cf) {
		o, inOld := of[name]
		c, inCur := cf[name]
		switch {
		case !inOld:
			out = append(out, FileChange{Name: name, Kind: FileAdded})
		case !inCur:
			out = append(out, FileChange{Name: name, Kind: FileRemoved})
		case o != c:
			out = append(out, FileChange{Name: name, Kind: FileModified})
		}
	}
	return out
}

func unionKeys[K cmp.Ordered, V any](a, b map[K]V) []K {
	set := make(map[K]struct{}, len(a)+len(b))
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	return slices.Sorted(maps.Keys(set))
}
