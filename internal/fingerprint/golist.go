// Package fingerprint describes what a test binary is built from, so that a
// change in the binary can be traced to the packages, modules or settings
// that caused it.
//
// The go command's own test ID hashes the whole build graph, including
// compiler flags and toolchain details. This package approximates it with
// what go list and go env report: the content of every local package in the
// test's dependency graph, the version of every module dependency, and the
// build-relevant settings. When those all match and the binary still
// changed, the cause is something outside this approximation.
package fingerprint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Santaniel0/uncached/internal/ids"
)

// Runner runs the go command and returns its standard output.
type Runner interface {
	Output(ctx context.Context, args ...string) ([]byte, error)
}

// GoEnv is the part of go env that uncached needs.
type GoEnv struct {
	GoCache      string `json:"GOCACHE"`
	GoCacheProg  string `json:"GOCACHEPROG"`
	GoMod        string `json:"GOMOD"`
	GoVersion    string `json:"GOVERSION"`
	GoOS         string `json:"GOOS"`
	GoArch       string `json:"GOARCH"`
	GoAMD64      string `json:"GOAMD64"`
	GoARM        string `json:"GOARM"`
	GoARM64      string `json:"GOARM64"`
	CgoEnabled   string `json:"CGO_ENABLED"`
	GoExperiment string `json:"GOEXPERIMENT"`
	GoFlags      string `json:"GOFLAGS"`
	CC           string `json:"CC"`
	CgoCFlags    string `json:"CGO_CFLAGS"`
	CgoLDFlags   string `json:"CGO_LDFLAGS"`
}

// settings returns the build-relevant variables by name, skipping unset
// ones.
func (e GoEnv) settings() map[string]string {
	all := map[string]string{
		"GOVERSION": e.GoVersion, "GOOS": e.GoOS, "GOARCH": e.GoArch,
		"GOAMD64": e.GoAMD64, "GOARM": e.GoARM, "GOARM64": e.GoARM64,
		"CGO_ENABLED": e.CgoEnabled, "GOEXPERIMENT": e.GoExperiment,
		"GOFLAGS": e.GoFlags, "CC": e.CC, "CGO_CFLAGS": e.CgoCFlags,
		"CGO_LDFLAGS": e.CgoLDFlags,
	}
	for k, v := range all {
		if v == "" {
			delete(all, k)
		}
	}
	return all
}

var goEnvVars = []string{
	"GOCACHE", "GOCACHEPROG", "GOMOD", "GOVERSION", "GOOS", "GOARCH", "GOAMD64",
	"GOARM", "GOARM64", "CGO_ENABLED", "GOEXPERIMENT", "GOFLAGS", "CC",
	"CGO_CFLAGS", "CGO_LDFLAGS",
}

// ReadEnv runs go env.
func ReadEnv(ctx context.Context, r Runner) (GoEnv, error) {
	out, err := r.Output(ctx, append([]string{"env", "-json"}, goEnvVars...)...)
	if err != nil {
		return GoEnv{}, fmt.Errorf("read go env: %w", err)
	}
	var env GoEnv
	if err := json.Unmarshal(out, &env); err != nil {
		return GoEnv{}, fmt.Errorf("decode go env: %w", err)
	}
	return env, nil
}

// Module is a module as reported by go list.
type Module struct {
	Path    string  `json:"Path"`
	Version string  `json:"Version"`
	Main    bool    `json:"Main"`
	Dir     string  `json:"Dir"`
	Replace *Module `json:"Replace"`
}

// PackageError is a load error reported by go list -e.
type PackageError struct {
	Err string `json:"Err"`
}

// Package is the subset of go list's output that the fingerprint uses.
type Package struct {
	ImportPath   ids.ImportPath   `json:"ImportPath"`
	Name         string           `json:"Name"`
	Dir          string           `json:"Dir"`
	Root         string           `json:"Root"`
	ForTest      string           `json:"ForTest"`
	Standard     bool             `json:"Standard"`
	Module       *Module          `json:"Module"`
	GoFiles      []string         `json:"GoFiles"`
	CgoFiles     []string         `json:"CgoFiles"`
	CFiles       []string         `json:"CFiles"`
	CXXFiles     []string         `json:"CXXFiles"`
	MFiles       []string         `json:"MFiles"`
	HFiles       []string         `json:"HFiles"`
	FFiles       []string         `json:"FFiles"`
	SFiles       []string         `json:"SFiles"`
	SwigFiles    []string         `json:"SwigFiles"`
	SwigCXXFiles []string         `json:"SwigCXXFiles"`
	SysoFiles    []string         `json:"SysoFiles"`
	EmbedFiles   []string         `json:"EmbedFiles"`
	Deps         []ids.ImportPath `json:"Deps"`
	Error        *PackageError    `json:"Error"`
}

// sourceFiles returns every file that goes into compiling the package,
// relative to its directory.
func (p Package) sourceFiles() []string {
	var out []string
	for _, list := range [][]string{
		p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles,
		p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles,
	} {
		out = append(out, list...)
	}
	return out
}

// local reports whether the package's files live in the developer's tree
// rather than in the module cache, so they are worth hashing.
func (p Package) local() bool {
	switch {
	case p.Standard:
		return false
	case p.Module == nil:
		return true // GOPATH mode or command-line-arguments
	case p.Module.Main:
		return true
	case p.Module.Replace != nil && p.Module.Replace.Version == "":
		return true // replaced by a directory
	}
	return false
}

// moduleVersion returns the version that identifies a non-local module.
func (p Package) moduleVersion() (ids.ModulePath, string) {
	m := p.Module
	if m.Replace != nil {
		return ids.ModulePath(m.Path), m.Replace.Path + "@" + m.Replace.Version
	}
	return ids.ModulePath(m.Path), m.Version
}

// TestMain is the import path go list uses for a package's test binary.
func TestMain(p ids.ImportPath) ids.ImportPath { return p + ".test" }

var listFields = strings.Join([]string{
	"ImportPath", "Name", "Dir", "Root", "ForTest", "Standard", "Module",
	"GoFiles", "CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles",
	"SFiles", "SwigFiles", "SwigCXXFiles", "SysoFiles", "EmbedFiles", "Deps", "Error",
}, ",")

// Windows limits a command line to 32K characters, so long package lists
// are split across several go list runs.
const listBatch = 200

// List runs go list -deps -test for the given packages. flags are build
// flags such as -tags that select files.
func List(ctx context.Context, r Runner, flags []string, pkgs []ids.ImportPath) (map[ids.ImportPath]Package, error) {
	out := make(map[ids.ImportPath]Package)
	for start := 0; start < len(pkgs); start += listBatch {
		batch := pkgs[start:min(start+listBatch, len(pkgs))]
		args := append([]string{"list", "-e", "-deps", "-test", "-json=" + listFields}, flags...)
		for _, p := range batch {
			args = append(args, string(p))
		}
		data, err := r.Output(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("list packages: %w", err)
		}
		if err := decodePackages(data, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func decodePackages(data []byte, into map[ids.ImportPath]Package) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var p Package
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode go list output: %w", err)
		}
		into[p.ImportPath] = p
	}
}
