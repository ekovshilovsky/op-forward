package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Compare returns -1 if a < b, 0 if a == b, 1 if a > b.
// Versions must be in "major.minor.patch" format (e.g., "0.3.0").
// Non-numeric versions like "dev" are treated as 0.0.0.
func Compare(a, b string) int {
	ap := parse(a)
	bp := parse(b)
	for i := 0; i < 3; i++ {
		if ap[i] < bp[i] {
			return -1
		}
		if ap[i] > bp[i] {
			return 1
		}
	}
	return 0
}

// CheckCompatibility evaluates a client version against server and minimum versions.
// Returns:
//   - upgradeRequired: true if clientVersion < minVersion (hard block)
//   - updateAvailable: non-empty string with server version if clientVersion < serverVersion
//   - message: human-readable explanation for the client
func CheckCompatibility(clientVersion, serverVersion, minVersion string) (upgradeRequired bool, updateAvailable string, message string) {
	if clientVersion == "" || clientVersion == "dev" {
		return false, "", ""
	}

	if Compare(clientVersion, minVersion) < 0 {
		return true, serverVersion, fmt.Sprintf(
			"client version %s is below minimum required version %s — please update with: op-forward update",
			clientVersion, minVersion,
		)
	}

	if Compare(clientVersion, serverVersion) < 0 {
		return false, serverVersion, ""
	}

	return false, "", ""
}

func parse(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}
		}
		result[i] = n
	}
	return result
}
