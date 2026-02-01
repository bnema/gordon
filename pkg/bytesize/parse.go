// Package bytesize provides human-friendly byte size parsing.
package bytesize

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// unitMultipliers maps human-friendly unit suffixes to their byte values.
var unitMultipliers = map[string]int64{
	"B":  1,
	"KB": 1 << 10, // 1024
	"MB": 1 << 20, // 1048576
	"GB": 1 << 30, // 1073741824
	"TB": 1 << 40, // 1099511627776
}

// Parse parses a human-friendly byte size string.
//
// Supported units: B, KB, MB, GB, TB (case-insensitive).
// The decimal prefix (1024-based) is used (KiB, MiB, etc.).
//
// Returns int64 to safely integrate with standard library functions like http.MaxBytesReader.
// Maximum representable value is 9,223,372,036,854,775,807 bytes (~8 exabytes).
//
// Examples:
//
//	Parse("512MB")  // 536870912 bytes
//	Parse("1GB")    // 1073741824 bytes
//	Parse("100KB")  // 102400 bytes
//	Parse("1.5GB")  // 1610612736 bytes
func Parse(s string) (int64, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	s = strings.ToUpper(s)

	// Find the unit suffix - try longest first to avoid matching "B" before "KB"
	units := []string{"TB", "GB", "MB", "KB", "B"}
	var unit string
	var valueStr string

	for _, u := range units {
		if strings.HasSuffix(s, u) {
			unit = u
			valueStr = strings.TrimSuffix(s, u)
			break
		}
	}

	if unit == "" {
		return 0, fmt.Errorf("invalid size %q: missing unit (supported: B, KB, MB, GB, TB)", s)
	}

	valueStr = strings.TrimSpace(valueStr)
	if valueStr == "" {
		return 0, fmt.Errorf("invalid size %q: missing numeric value", s)
	}

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value %q in %q: %w", valueStr, s, err)
	}

	if value < 0 {
		return 0, fmt.Errorf("invalid size %q: negative value not allowed", s)
	}

	multiplier := unitMultipliers[unit]
	result := value * float64(multiplier)

	// Check for overflow before converting to int64
	if result > math.MaxInt64 {
		return 0, fmt.Errorf("size %q exceeds maximum allowed value (9.2 EB)", s)
	}

	return int64(result), nil
}
