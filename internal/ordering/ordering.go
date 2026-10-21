// Package ordering implements natural ("human") ordering of tag strings.
//
// It is used as a fallback when a registry cannot tell us when each tag was
// pushed. Runs of digits are compared by numeric value so "v3.10" sorts after
// "v3.9".
package ordering

import "strings"

// NaturalCmp compares a and b, treating runs of ASCII digits as numbers. It
// returns a negative number when a sorts before b, zero when they are equal and
// a positive number when a sorts after b.
func NaturalCmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			ai, bj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			x := strings.TrimLeft(a[ai:i], "0")
			y := strings.TrimLeft(b[bj:j], "0")
			if len(x) != len(y) {
				return sign(len(x) - len(y))
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			continue
		}
		if a[i] != b[j] {
			return sign(int(a[i]) - int(b[j]))
		}
		i++
		j++
	}
	// Whichever string still has bytes left sorts after the other.
	return sign((len(a) - i) - (len(b) - j))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
