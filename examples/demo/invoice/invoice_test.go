package invoice

import (
	"os"
	"testing"
)

func TestRender(t *testing.T) {
	want, err := os.ReadFile("testdata/invoice.golden")
	if err != nil {
		t.Fatal(err)
	}
	got := Render([]Line{{"apples", 350}, {"pears", 420}})
	if got != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
