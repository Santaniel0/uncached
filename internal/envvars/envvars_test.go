package envvars

import (
	"runtime"
	"slices"
	"testing"
)

func TestLookup(t *testing.T) {
	env := []string{"A=1", "B=", "A=2", "C=x=y", "broken"}
	tests := []struct {
		key    string
		want   string
		wantOK bool
	}{
		{"A", "2", true},
		{"B", "", true},
		{"C", "x=y", true},
		{"D", "", false},
		{"broken", "", false},
	}
	for _, tt := range tests {
		got, ok := Lookup(env, tt.key)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("Lookup(%q) = %q, %v; want %q, %v", tt.key, got, ok, tt.want, tt.wantOK)
		}
	}
	if v, ok := Lookup(env, "a"); runtime.GOOS == "windows" != ok || (ok && v != "2") {
		t.Errorf("Lookup(\"a\") = %q, %v on %s", v, ok, runtime.GOOS)
	}
}

func TestSet(t *testing.T) {
	got := Set([]string{"A=1", "B=2", "A=3"}, "A", "4")
	if want := []string{"B=2", "A=4"}; !slices.Equal(got, want) {
		t.Errorf("Set = %v, want %v", got, want)
	}
	if v, _ := LookupFunc(got)("A"); v != "4" {
		t.Errorf("LookupFunc(A) = %q", v)
	}
}
