// Package rediscompat contains small server-version helpers shared by optional
// Redis adapters. It has no Redis client dependency.
package rediscompat

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed Redis server version. Missing patch components are zero.
type Version struct {
	Major int
	Minor int
	Patch int
}

// Parse accepts major.minor or major.minor.patch.
func Parse(value string) (Version, error) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, fmt.Errorf("redis version %q must use major.minor[.patch]", value)
	}
	values := [3]int{}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return Version{}, fmt.Errorf("redis version %q is invalid", value)
		}
		values[index] = number
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

// Compare returns -1, 0, or 1 according to the ordering of a and b.
func Compare(a, b Version) int {
	if a.Major != b.Major {
		if a.Major < b.Major {
			return -1
		}
		return 1
	}
	if a.Minor != b.Minor {
		if a.Minor < b.Minor {
			return -1
		}
		return 1
	}
	if a.Patch < b.Patch {
		return -1
	}
	if a.Patch > b.Patch {
		return 1
	}
	return 0
}
