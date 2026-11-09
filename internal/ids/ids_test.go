package ids

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseActionID(t *testing.T) {
	good := strings.Repeat("0a", 32)
	tests := []struct {
		in      string
		wantErr bool
	}{
		{good, false},
		{strings.ToUpper(good), false},
		{good[:62], true},
		{good + "00", true},
		{strings.Repeat("zz", 32), true},
		{"", true},
	}
	for _, tt := range tests {
		id, err := ParseActionID(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrBadActionID) {
				t.Errorf("ParseActionID(%q) error = %v, want ErrBadActionID", tt.in, err)
			}
			continue
		}
		if err != nil || id.String() != strings.ToLower(tt.in) || id.IsZero() {
			t.Errorf("ParseActionID(%q) = %v, %v", tt.in, id, err)
		}
	}
}

func TestActionIDJSON(t *testing.T) {
	id, err := ParseActionID(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]ActionID{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]ActionID
	if err := json.Unmarshal(b, &back); err != nil || back["id"] != id {
		t.Errorf("round trip gave %v, %v from %s", back, err, b)
	}
	if err := json.Unmarshal([]byte(`{"id":"nope"}`), &back); !errors.Is(err, ErrBadActionID) {
		t.Errorf("bad ID decoded with err = %v", err)
	}
}

func TestBase(t *testing.T) {
	tests := map[ImportPath]ImportPath{
		"example.com/p":                           "example.com/p",
		"example.com/p [example.com/p.test]":      "example.com/p",
		"example.com/p_test [example.com/p.test]": "example.com/p_test",
	}
	for in, want := range tests {
		if got := in.Base(); got != want {
			t.Errorf("%q.Base() = %q, want %q", in, got, want)
		}
	}
}
