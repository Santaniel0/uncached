package config

import "testing"

func TestRegion(t *testing.T) {
	if Region() == "" {
		t.Fatal("empty region")
	}
}
