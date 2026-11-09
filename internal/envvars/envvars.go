// Package envvars works with environment lists in os.Environ form
// ("KEY=value"), so that the environment can be passed around explicitly
// instead of being read from the process.
package envvars

import (
	"runtime"
	"strings"
)

// sameKey compares variable names the way the OS does: Windows ignores case.
func sameKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Lookup returns the value of key in env. Later entries win, matching
// os/exec, which keeps the last duplicate.
func Lookup(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := strings.Cut(env[i], "=")
		if ok && sameKey(k, key) {
			return v, true
		}
	}
	return "", false
}

// Set returns a copy of env with key set to value and earlier copies of key
// removed.
func Set(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if sameKey(k, key) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}

// LookupFunc adapts env to the signature of os.LookupEnv.
func LookupFunc(env []string) func(string) (string, bool) {
	return func(key string) (string, bool) { return Lookup(env, key) }
}
