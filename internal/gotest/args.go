// Package gotest runs go test with the test cache's debug output enabled and
// turns what it prints into typed results and cache events.
package gotest

import (
	"slices"
	"strings"
)

// Flag is a go test or build flag that changes the test binary or the test
// cache key.
type Flag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (f Flag) String() string { return "-" + f.Name + "=" + f.Value }

// Args is a go test command line split into what matters for caching.
type Args struct {
	Raw []string
	// Flags holds the flags that affect the test binary or the cache key,
	// sorted by name.
	Flags []Flag
	// Patterns holds the arguments that are not flags or flag values.
	Patterns []string
}

// valueFlags take their value from the next argument when written without
// "=". Knowing them is what keeps "-run TestX" from reading as a package
// pattern.
var valueFlags = map[string]bool{
	"asmflags": true, "bench": true, "benchtime": true, "blockprofile": true,
	"blockprofilerate": true, "C": true, "compiler": true, "count": true,
	"covermode": true, "coverpkg": true, "coverprofile": true, "cpu": true,
	"cpuprofile": true, "exec": true, "fuzz": true, "fuzzminimizetime": true,
	"fuzztime": true, "gccgoflags": true, "gcflags": true, "installsuffix": true,
	"ldflags": true, "list": true, "memprofile": true, "memprofilerate": true,
	"mod": true, "modfile": true, "mutexprofile": true, "mutexprofilefraction": true,
	"o": true, "outputdir": true, "overlay": true, "p": true, "parallel": true,
	"pgo": true, "pkgdir": true, "run": true, "shuffle": true, "skip": true,
	"tags": true, "timeout": true, "toolexec": true, "trace": true, "vet": true,
	"buildmode": true,
}

// keyFlags change the compiled test binary or are part of the test ID
// (see the cacheable flag list in src/cmd/go/internal/test/test.go).
var keyFlags = map[string]bool{
	"asan": true, "asmflags": true, "buildmode": true, "compiler": true,
	"cover": true, "covermode": true, "coverpkg": true, "gccgoflags": true,
	"gcflags": true, "installsuffix": true, "ldflags": true, "linkshared": true,
	"mod": true, "modfile": true, "msan": true, "overlay": true, "pgo": true,
	"race": true, "tags": true, "toolexec": true, "trimpath": true,
	"benchtime": true, "count": true, "cpu": true, "exec": true, "failfast": true,
	"fullpath": true, "list": true, "parallel": true, "run": true, "short": true,
	"skip": true, "timeout": true, "v": true,
}

// listFlags select files and dependencies, so go list needs them to see the
// same packages go test built.
var listFlags = map[string]bool{
	"asan": true, "cover": true, "covermode": true, "coverpkg": true, "mod": true,
	"modfile": true, "msan": true, "overlay": true, "pgo": true, "race": true,
	"tags": true,
}

// ParseArgs splits a go test command line. Everything after -args belongs to
// the test binary and is not interpreted.
func ParseArgs(raw []string) Args {
	a := Args{Raw: slices.Clone(raw)}
	for i := 0; i < len(raw); i++ {
		arg := raw[i]
		if arg == "-args" || arg == "--args" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			a.Patterns = append(a.Patterns, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		name = strings.TrimPrefix(name, "test.")
		if !hasValue {
			if valueFlags[name] && i+1 < len(raw) {
				i++
				value = raw[i]
			} else {
				value = "true"
			}
		}
		if keyFlags[name] {
			a.Flags = append(a.Flags, Flag{Name: name, Value: value})
		}
	}
	slices.SortStableFunc(a.Flags, func(x, y Flag) int { return strings.Compare(x.Name, y.Name) })
	return a
}

// ListFlags returns the flags to pass to go list so that it resolves the
// same files and dependencies as the go test run.
func (a Args) ListFlags() []string {
	var out []string
	for _, f := range a.Flags {
		if listFlags[f.Name] {
			out = append(out, f.String())
		}
	}
	return out
}
