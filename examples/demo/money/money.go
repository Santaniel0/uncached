// Package money formats amounts of money.
package money

import "fmt"

const symbol = "EUR"

// Format renders an amount given in cents.
func Format(cents int) string {
	return fmt.Sprintf("%d.%02d %s", cents/100, cents%100, symbol)
}
