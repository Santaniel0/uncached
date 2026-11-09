// Package catalog loads product fixtures.
package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Product is one catalog entry.
type Product struct {
	Name  string `json:"name"`
	Cents int    `json:"cents"`
}

// Load reads every JSON file in dir.
func Load(dir string) ([]Product, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Product
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var p Product
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
