package fingerprint

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/ids"
)

type fakeRunner struct {
	out   map[string]string
	calls [][]string
}

func (f *fakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if out, ok := f.out[args[0]]; ok {
		return []byte(out), nil
	}
	return nil, errors.New("unexpected command")
}

func TestReadEnvAndConfig(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"env": `{"GOCACHE":"/c","GOVERSION":"go1.27.3","GOOS":"linux","GOARCH":"amd64","CGO_ENABLED":"0","GOFLAGS":""}`,
	}}
	env, err := ReadEnv(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if env.GoCache != "/c" || env.GoVersion != "go1.27.3" {
		t.Errorf("env = %+v", env)
	}
	cfg := NewConfig(env, []gotest.Flag{{Name: "race", Value: "true"}})
	want := Config{"GOVERSION": "go1.27.3", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0", "-race": "true"}
	if len(cfg) != len(want) {
		t.Fatalf("config = %v, want %v", cfg, want)
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("config[%s] = %q, want %q", k, cfg[k], v)
		}
	}
}

func TestListDecodesConcatenatedJSON(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"list": `{"ImportPath":"example.com/a","Dir":"/m/a","Module":{"Path":"example.com","Main":true}}
{"ImportPath":"example.com/a.test","Deps":["example.com/a [example.com/a.test]","fmt"]}
`}}
	pkgs, err := List(context.Background(), r, []string{"-tags=x"}, []ids.ImportPath{"example.com/a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs["example.com/a"].Dir != "/m/a" || !pkgs["example.com/a"].Module.Main {
		t.Errorf("packages = %+v", pkgs)
	}
	args := r.calls[0]
	if !slices.Contains(args, "-tags=x") || args[len(args)-1] != "example.com/a" || !strings.HasPrefix(args[4], "-json=") {
		t.Errorf("go list args = %v", args)
	}
}

func TestListBatches(t *testing.T) {
	r := &fakeRunner{out: map[string]string{"list": ""}}
	pkgs := make([]ids.ImportPath, listBatch+1)
	for i := range pkgs {
		pkgs[i] = "example.com/p"
	}
	if _, err := List(context.Background(), r, nil, pkgs); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Errorf("go list ran %d times, want 2", len(r.calls))
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// module lays out a two-package module with a module dependency and returns
// what go list -deps -test would report for testing example.com/m/b.
func module(t *testing.T) (string, map[ids.ImportPath]Package) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "a.go"), "package a")
	writeFile(t, filepath.Join(root, "b", "b.go"), "package b")
	writeFile(t, filepath.Join(root, "b", "b_test.go"), "package b")
	writeFile(t, filepath.Join(root, "b", "testdata", "embed.txt"), "e")
	mainMod := &Module{Path: "example.com/m", Main: true}
	listed := map[ids.ImportPath]Package{
		"example.com/m/a": {ImportPath: "example.com/m/a", Dir: filepath.Join(root, "a"), Module: mainMod, GoFiles: []string{"a.go"}},
		"example.com/m/b [example.com/m/b.test]": {
			ImportPath: "example.com/m/b [example.com/m/b.test]", Dir: filepath.Join(root, "b"), Module: mainMod,
			GoFiles: []string{"b.go", "b_test.go"}, EmbedFiles: []string{"testdata/embed.txt"},
		},
		"golang.org/x/text/width": {ImportPath: "golang.org/x/text/width", Module: &Module{Path: "golang.org/x/text", Version: "v0.20.0"}},
		"github.com/fork/lib":     {ImportPath: "github.com/fork/lib", Module: &Module{Path: "github.com/orig/lib", Version: "v1.0.0", Replace: &Module{Path: "github.com/fork/lib", Version: "v1.0.1"}}},
		"fmt":                     {ImportPath: "fmt", Standard: true},
		"example.com/m/b.test": {
			ImportPath: "example.com/m/b.test",
			Deps: []ids.ImportPath{
				"example.com/m/a", "example.com/m/b [example.com/m/b.test]",
				"fmt", "github.com/fork/lib", "golang.org/x/text/width", "not/listed",
			},
		},
	}
	return root, listed
}

func TestBuild(t *testing.T) {
	_, listed := module(t)
	tests, files, err := Build(context.Background(), listed, []ids.ImportPath{"example.com/m/b", "example.com/m/notest"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 1 {
		t.Fatalf("got fingerprints for %d tests, want 1", len(tests))
	}
	fp := tests["example.com/m/b"]
	if got := slices.Sorted(maps.Keys(fp.Sources)); !slices.Equal(got, []ids.ImportPath{"example.com/m/a", "example.com/m/b [example.com/m/b.test]"}) {
		t.Errorf("sources = %v", got)
	}
	if fp.Modules["golang.org/x/text"] != "v0.20.0" || fp.Modules["github.com/orig/lib"] != "github.com/fork/lib@v1.0.1" {
		t.Errorf("modules = %v", fp.Modules)
	}
	bFiles := files["example.com/m/b [example.com/m/b.test]"]
	if len(bFiles) != 3 || bFiles["testdata/embed.txt"] == "" {
		t.Errorf("files for b = %v", bFiles)
	}
}

func TestCompare(t *testing.T) {
	root, listed := module(t)
	snapshot := func(cfg Config) Side {
		tests, files, err := Build(context.Background(), listed, []ids.ImportPath{"example.com/m/b"}, 2)
		if err != nil {
			t.Fatal(err)
		}
		return Side{Test: tests["example.com/m/b"], Config: cfg, Files: files}
	}
	base := Config{"GOVERSION": "go1.27.3"}
	old := snapshot(base)

	if d := Compare(old, snapshot(base)); len(d) != 0 {
		t.Fatalf("identical fingerprints differ: %+v", d)
	}

	writeFile(t, filepath.Join(root, "a", "a.go"), "package a // edited")
	writeFile(t, filepath.Join(root, "a", "new.go"), "package a")
	a := listed["example.com/m/a"]
	a.GoFiles = []string{"a.go", "new.go"}
	listed["example.com/m/a"] = a
	lib := listed["golang.org/x/text/width"]
	lib.Module = &Module{Path: "golang.org/x/text", Version: "v0.21.0"}
	listed["golang.org/x/text/width"] = lib
	cur := snapshot(Config{"GOVERSION": "go1.27.4", "-race": "true"})

	got := Compare(old, cur)
	want := []Diff{
		{Kind: DiffPackageChanged, Subject: "example.com/m/a", Files: []FileChange{
			{Name: "a.go", Kind: FileModified},
			{Name: "new.go", Kind: FileAdded},
		}},
		{Kind: DiffModuleChanged, Subject: "golang.org/x/text", Old: "v0.20.0", New: "v0.21.0"},
		{Kind: DiffSettingChanged, Subject: "-race", New: "true"},
		{Kind: DiffSettingChanged, Subject: "GOVERSION", Old: "go1.27.3", New: "go1.27.4"},
	}
	if len(got) != len(want) {
		t.Fatalf("Compare:\n got %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Subject != want[i].Subject ||
			got[i].Old != want[i].Old || got[i].New != want[i].New || !slices.Equal(got[i].Files, want[i].Files) {
			t.Errorf("diff %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestCompareSkipsFileDetailWhenFilesAreStale(t *testing.T) {
	old := Side{
		Test:  Test{Sources: map[ids.ImportPath]string{"p": "digest-from-an-older-run"}},
		Files: Files{"p": {"a.go": "x"}},
	}
	cur := Side{
		Test:  Test{Sources: map[ids.ImportPath]string{"p": packageDigest(map[string]string{"a.go": "y"})}},
		Files: Files{"p": {"a.go": "y"}},
	}
	d := Compare(old, cur)
	if len(d) != 1 || d[0].Kind != DiffPackageChanged || d[0].Files != nil {
		t.Errorf("diffs = %+v", d)
	}
}

func TestPackageAddedAndRemoved(t *testing.T) {
	old := Side{Test: Test{Sources: map[ids.ImportPath]string{"p": "1"}, Modules: map[ids.ModulePath]string{"m": "v1"}}}
	cur := Side{Test: Test{Sources: map[ids.ImportPath]string{"q": "1"}, Modules: map[ids.ModulePath]string{"n": "v2"}}}
	var kinds []DiffKind
	for _, d := range Compare(old, cur) {
		kinds = append(kinds, d.Kind)
	}
	want := []DiffKind{DiffPackageAdded, DiffPackageRemoved, DiffModuleAdded, DiffModuleRemoved}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
}
