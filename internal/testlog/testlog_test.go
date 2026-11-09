package testlog

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    []Entry
		wantErr error
	}{
		{
			name: "typical",
			data: "# test log\nopen testdata/in.txt\ngetenv PROBE_MODE\nstat /tmp/x y\n",
			want: []Entry{
				{OpOpen, "testdata/in.txt"},
				{OpGetenv, "PROBE_MODE"},
				{OpStat, "/tmp/x y"},
			},
		},
		{
			name: "empty log",
			data: "# test log\n",
		},
		{
			name:    "no header",
			data:    "open a\n",
			wantErr: ErrMalformed,
		},
		{
			name:    "line without name",
			data:    "# test log\nopen\n",
			wantErr: ErrMalformed,
		},
		{
			name:    "unknown op",
			data:    "# test log\nunlink a\n",
			wantErr: ErrUnknownOp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.data))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mod")
	pkg := filepath.Join(root, "a")
	elsewhere := filepath.Join(filepath.Dir(root), "outside")
	abs := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }

	entries := []Entry{
		{OpOpen, "testdata/in.txt"},
		{OpGetenv, "HOME"},
		{OpOpen, "testdata/in.txt"},
		{OpStat, filepath.Join(elsewhere, "f")},
		{OpOpen, "../b/data.json"},
		{OpChdir, filepath.Join(root, "c")},
		{OpStat, "golden"},
		{OpChdir, elsewhere},
		{OpOpen, "x"},
		{OpGetenv, "GODEBUG"},
	}
	got := Resolve(entries, pkg, root)
	want := []Input{
		GODEBUG,
		{OpOpen, abs("a", "testdata", "in.txt")},
		{OpGetenv, "HOME"},
		{OpOpen, abs("b", "data.json")},
		{OpChdir, abs("c")},
		{OpStat, abs("c", "golden")},
		{OpChdir, elsewhere},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Resolve:\n got %v\nwant %v", got, want)
	}
}

func TestResolveWithoutRootKeepsOnlyEnvAndChdir(t *testing.T) {
	dir := t.TempDir()
	got := Resolve([]Entry{{OpOpen, "a"}, {OpGetenv, "X"}, {OpChdir, dir}}, dir, "")
	want := []Input{GODEBUG, {OpGetenv, "X"}, {OpChdir, filepath.Clean(dir)}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/work/mod")
	if runtime.GOOS == "windows" {
		root = `C:\work\mod`
	}
	tests := []struct {
		path string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "a", "b"), true},
		{filepath.Join(root, "..", "mod2"), false},
		{filepath.Dir(root), false},
		{filepath.Join(root, "..mod"), true},
	}
	for _, tt := range tests {
		if got := Within(root, tt.path); got != tt.want {
			t.Errorf("Within(%q, %q) = %v, want %v", root, tt.path, got, tt.want)
		}
	}
	if runtime.GOOS == "windows" {
		if !Within(`C:\Work\Mod`, `c:\work\mod\a`) {
			t.Error("Within should ignore case on Windows")
		}
	}
}

func TestOpJSON(t *testing.T) {
	b, err := json.Marshal(Input{Op: OpChdir, Name: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"op":"chdir","name":"/x"}` {
		t.Errorf("marshal = %s", b)
	}
	var in Input
	if err := json.Unmarshal([]byte(`{"op":"stat","name":"y"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in != (Input{OpStat, "y"}) {
		t.Errorf("unmarshal = %v", in)
	}
	if err := json.Unmarshal([]byte(`{"op":"rm","name":"y"}`), &in); !errors.Is(err, ErrUnknownOp) {
		t.Errorf("unmarshal of unknown op: err = %v", err)
	}
	if _, err := json.Marshal(Op(0)); err == nil {
		t.Error("marshaling the zero Op should fail")
	}
}
