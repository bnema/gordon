package compatoldnew

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	LabelRun     = "gordon.compat.run"
	LabelSide    = "gordon.compat.side"
	LabelFixture = "gordon.compat.fixture"

	SideOld = "old"
	SideNew = "new"
)

const maxResourceNameLen = 63

// RunID returns a deterministic-test-name-derived identifier with a unique suffix.
func RunID(testName string) string {
	base := sanitizePart(testName)
	if base == "" {
		base = "run"
	}
	suffix := fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), randomHex(4))
	return trimResourceName(base, len(suffix)+1) + "-" + suffix
}

func ContainerPrefix(runID, side string) string {
	return resourcePrefix(runID, side)
}

func NetworkPrefix(runID, side string) string {
	return resourcePrefix(runID, side)
}

func VolumePrefix(runID, side string) string {
	return resourcePrefix(runID, side)
}

func ResourceLabels(runID, side, fixture string) map[string]string {
	return map[string]string{
		LabelRun:     sanitizePart(runID),
		LabelSide:    sanitizePart(side),
		LabelFixture: sanitizePart(fixture),
	}
}

func resourcePrefix(runID, side string) string {
	prefix := "gordon-compat-"
	side = sanitizePart(side)
	if side == "" {
		side = "side"
	}
	middle := trimResourceName(sanitizePart(runID), len(prefix)+len(side)+1)
	if middle == "" {
		middle = "run"
	}
	return prefix + middle + "-" + side
}

func sanitizePart(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r)
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func trimResourceName(s string, reserved int) string {
	limit := maxResourceNameLen - reserved
	if limit < 1 {
		limit = 1
	}
	if len(s) <= limit {
		return s
	}
	return strings.Trim(s[:limit], "-")
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buf)
}
