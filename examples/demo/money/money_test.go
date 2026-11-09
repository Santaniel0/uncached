package money

import "testing"

func TestFormat(t *testing.T) {
	if got := Format(1234); got != "12.34 EUR" {
		t.Fatalf("Format(1234) = %q", got)
	}
}
