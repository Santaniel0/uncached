package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"4MiB", 4 << 20, false},
		{"512KiB", 512 << 10, false},
		{"4M", 4 << 20, false},
		{"1G", 1 << 30, false},
		{"100B", 100, false},
		{"1048576", 1 << 20, false},
		{" 2 MiB ", 2 << 20, false},
		{"0", 0, false},
		{"", 0, true},
		{"MiB", 0, true},
		{"-1K", 0, true},
		{"1.5M", 0, true},
		{"99999999999999GiB", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSize(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseSize(%q) = %d, %v; want %d, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestRunUsage(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"no command", nil, exitError, "", "Usage:"},
		{"help", []string{"help"}, 0, "Usage:", ""},
		{"unknown command", []string{"frobnicate"}, exitError, "", `unknown command "frobnicate"`},
		{"diff needs two files", []string{"diff", "a.json"}, exitError, "", "need exactly two state files"},
		{"diff of missing files", []string{"diff", "missing-a.json", "missing-b.json"}, exitError, "", "no saved state"},
		{"bad flag", []string{"run", "-nope"}, exitError, "", "flag provided but not defined"},
		{"command help", []string{"restore-mtime", "-h"}, 0, "", "Usage: uncached restore-mtime"},
		{"bad size", []string{"run", "-content-limit", "lots"}, exitError, "", `invalid size "lots"`},
		{"extra arguments", []string{"explain", "x"}, exitError, "", "unexpected arguments: x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), tt.wantOut)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tt.wantErr)
			}
		})
	}
}
