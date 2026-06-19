package ledger

import "fmt"

// Money is always represented as an int64 number of *minor units* (e.g. cents
// for HKD/USD). We never use float64 for money: floating point cannot represent
// values like 0.10 exactly, so repeated arithmetic drifts and you lose cents —
// an unforgivable bug in a ledger. Keep money as integers everywhere and only
// format to a decimal string at the very edge (display / logging).

// FormatMinor renders minor units as a fixed 2-decimal string.
//
//	FormatMinor(12345) == "123.45"
//	FormatMinor(-250)  == "-2.50"
func FormatMinor(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := fmt.Sprintf("%d.%02d", minor/100, minor%100)
	if neg {
		return "-" + s
	}
	return s
}
