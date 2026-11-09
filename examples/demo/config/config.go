// Package config reads deployment settings from the environment.
package config

import "os"

// Region returns the region to deploy to.
func Region() string {
	if r := os.Getenv("DEMO_REGION"); r != "" {
		return r
	}
	return "eu-west"
}
