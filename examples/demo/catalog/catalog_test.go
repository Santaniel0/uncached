package catalog

import "testing"

func TestLoad(t *testing.T) {
	products, err := Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) == 0 {
		t.Fatal("no products")
	}
}
