// Package duration provides human-friendly duration parsing.
package duration

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Common duration constants for human-friendly units.
const (
	Day   = 24 * time.Hour
	Week  = 7 * Day
	Month = 30 * Day  // Approximate
	Year  = 365 * Day // Approximate
)

// unitMultipliers maps human-friendly unit suffixes to their duration values.
var unitMultipliers = map[string]time.Duration{
	"d": Day,
	"w": Week,
	"M": Month, // Capital M for months to distinguish from minutes
	"y": Year,
}

// durationPattern matches duration components like "1y", "6M", "2w", "3d".
var durationPattern = regexp.MustCompile(`(\d+)([ywMd])`)

// Parse extends time.ParseDuration to support human-friendly units:
//   - d (days) = 24h
//   - w (weeks) = 7d
//   - M (months) = 30d (approximate)
//   - y (years) = 365d (approximate)
//
// Standard Go duration units (ns, us, ms, s, m, h) are also supported.
// Compound durations like "1y6M" or "2w3d" or "1d12h" work as expected.
//
// Special case: "0" returns 0 duration (useful for never-expiring tokens).
//
// Examples:
//
//	Parse("1d")      // 24 hours
//	Parse("2w")      // 14 days
//	Parse("1M")      // 30 days
//	Parse("1y")      // 365 days
//	Parse("1y6M")    // 1 year and 6 months
//	Parse("2w3d")    // 2 weeks and 3 days
//	Parse("1d12h")   // 1 day and 12 hours
//	Parse("24h")     // standard Go duration
//	Parse("0")       // zero duration
func Parse(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)

	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	// Special case: "0" means no expiry
	if s == "0" {
		return 0, nil
	}

	// Check if the string contains any human-friendly units
	if !containsHumanUnits(s) {
		// Fall back to standard Go parsing
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w (supported units: ns, us, ms, s, m, h, d, w, M, y)", s, err)
		}
		return d, nil
	}

	// Parse human-friendly units
	return parseHumanDuration(s)
}

// containsHumanUnits checks if the string contains any human-friendly unit suffixes.
func containsHumanUnits(s string) bool {
	for unit := range unitMultipliers {
		if strings.Contains(s, unit) {
			return true
		}
	}
	return false
}

// parseHumanDuration parses a duration string containing human-friendly units.
func parseHumanDuration(s string) (time.Duration, error) {
	var total time.Duration
	remaining := s

	// First, extract all human-friendly units (y, M, w, d)
	matches := durationPattern.FindAllStringSubmatch(remaining, -1)
	if len(matches) > 0 {
		for _, match := range matches {
			value, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration value %q in %q", match[1], s)
			}

			unit := match[2]
			multiplier, ok := unitMultipliers[unit]
			if !ok {
				return 0, fmt.Errorf("unknown duration unit %q in %q", unit, s)
			}

			total += time.Duration(value) * multiplier
		}

		// Remove the matched human-friendly parts
		remaining = durationPattern.ReplaceAllString(remaining, "")
	}

	// If there's remaining content, try to parse it as standard Go duration
	remaining = strings.TrimSpace(remaining)
	if remaining != "" {
		d, err := time.ParseDuration(remaining)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w (supported units: ns, us, ms, s, m, h, d, w, M, y)", s, err)
		}
		total += d
	}

	return total, nil
}
