// Package invoice renders invoices.
package invoice

import (
	"fmt"
	"strings"

	"example.com/demo/money"
)

// Line is one invoice line.
type Line struct {
	Item  string
	Cents int
}

// Render returns the invoice as plain text.
func Render(lines []Line) string {
	var b strings.Builder
	total := 0
	for _, l := range lines {
		fmt.Fprintf(&b, "%-10s %s\n", l.Item, money.Format(l.Cents))
		total += l.Cents
	}
	fmt.Fprintf(&b, "%-10s %s\n", "total", money.Format(total))
	return b.String()
}
