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
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 0 || r.CPUs >= float64(math.MaxInt64)/1e9 || (r.CPUs > 0 && r.CPUs < 1e-9) {
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
