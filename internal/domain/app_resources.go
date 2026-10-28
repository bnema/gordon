package domain

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// AppResources holds optional CPU quotas and byte limits. Zero means unset.
// MemorySwap is the total memory plus swap allowance, as in the runtime API.
type AppResources struct {
	CPUs       float64 `json:"cpus,omitempty"`
	Memory     int64   `json:"memory,omitempty"`
	MemorySwap int64   `json:"memory_swap,omitempty"`
}

var resourceSizePattern = regexp.MustCompile(`^([0-9]+)([bBkKmMgG])$`)

// ParseResourceSize parses positive byte sizes with binary b/k/m/g units.
func ParseResourceSize(value string) (int64, error) {
	match := resourceSizePattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("must be a positive size with b/k/m/g units")
	}
	n, err := strconv.ParseInt(match[1], 10, 64)
	shift := uint(strings.Index("bkmg", strings.ToLower(match[2])) * 10)
	if err != nil || n <= 0 || n > math.MaxInt64>>shift {
		return 0, fmt.Errorf("must be a positive size within int64 bytes")
	}
	return n << shift, nil
}

// Validate checks normalized resource limits; explicit zero is rejected during decoding.
func (r AppResources) Validate(service string) error {
	if _, err := CPUsToNanoCPUs(r.CPUs); err != nil {
		return fmt.Errorf("%w: service %q resources.cpus must be positive and representable as NanoCPUs", ErrInvalidAppSpec, service)
	}
	if r.Memory < 0 {
		return fmt.Errorf("%w: service %q resources.memory must be positive", ErrInvalidAppSpec, service)
	}
	if r.MemorySwap < 0 || (r.MemorySwap > 0 && (r.Memory == 0 || r.MemorySwap < r.Memory)) {
		return fmt.Errorf("%w: service %q resources.memory_swap requires memory and must be >= memory", ErrInvalidAppSpec, service)
	}
	return nil
}

// ResourceView omits an entirely unlimited resource set from JSON.
func (r AppResources) ResourceView() *AppResources {
	if r == (AppResources{}) {
		return nil
	}
	return &r
}

// CPUsToNanoCPUs converts cores to the nearest NanoCPU, rounding halves up.
// Zero means unset/unlimited. Positive quotas must round to at least one
// NanoCPU; non-finite, negative, and overflowing values are rejected.
func CPUsToNanoCPUs(cpus float64) (int64, error) {
	if math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus < 0 {
		return 0, fmt.Errorf("CPU quota must be finite and non-negative")
	}
	rounded := math.Round(cpus * 1e9)
	// float64(MaxInt64) is 2^63, already outside the int64 range.
	if rounded >= float64(math.MaxInt64) || (cpus > 0 && rounded < 1) {
		return 0, fmt.Errorf("CPU quota must round to 1..MaxInt64 NanoCPUs")
	}
	return int64(rounded), nil
}
