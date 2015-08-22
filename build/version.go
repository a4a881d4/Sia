package build

import (
	"strconv"
	"strings"
)

// Version is the current version of siad.
const Version = "0.4.0"

// IsVersion returns whether str is a valid version number.
func IsVersion(str string) bool {
	for _, n := range strings.Split(str, ".") {
		if _, err := strconv.Atoi(n); err != nil {
			return false
		}
	}
	return true
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// VersionCmp returns an int indicating the difference between a and b. It
// follows the convention of bytes.Compare and big.Cmp:
//
//   -1 if a <  b
//    0 if a == b
//   +1 if a >  b
//
// One important quirk is that "1.1.0" is considered newer than "1.1", despite
// being numerically equal.
func VersionCmp(a, b string) int {
	aNums := strings.Split(a, ".")
	bNums := strings.Split(b, ".")
	for i := 0; i < min(len(aNums), len(bNums)); i++ {
		// assume that both version strings are valid
		aInt, _ := strconv.Atoi(aNums[i])
		bInt, _ := strconv.Atoi(bNums[i])
		if aInt < bInt {
			return -1
		} else if aInt > bInt {
			return 1
		}
	}
	// all shared digits are equal, but lengths may not be equal
	if len(aNums) < len(bNums) {
		return -1
	} else if len(aNums) > len(bNums) {
		return 1
	}
	// strings are identical
	return 0
}