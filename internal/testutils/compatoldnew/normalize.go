package compatoldnew

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var meaningfulStringPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsha256:[0-9a-f]{64}\b`),
	regexp.MustCompile(`(?i)\benv[-_]?hash\b\s*[:=]\s*"?[0-9a-f]{12,64}"?`),
}

var normalizers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\b[0-9a-f]{64}\b`), "<image-id>"},
	{regexp.MustCompile(`\b[0-9a-f]{12,63}\b`), "<container-id>"},
	{regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b20\d\d-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z\b`), "<timestamp>"},
	{regexp.MustCompile(`\b127\.0\.0\.1:\d{2,5}\b`), "127.0.0.1:<port>"},
	{regexp.MustCompile(`\blocalhost:\d{2,5}\b`), "localhost:<port>"},
	{regexp.MustCompile(`/tmp/[^\s"']+`), "<tmp-path>"},
	{regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|s|µs|us|ns)\b`), "<duration>"},
}

var forbiddenKeys = map[string]bool{
	"status": true, "statusCode": true, "exitCode": true, "labels": true,
	"networks": true, "network": true, "envHash": true, "domain": true, "digest": true,
}

// Normalize returns a stable representation while preserving security/API-significant values.
func Normalize(v any) any {
	switch x := v.(type) {
	case string:
		return normalizeString(x)
	case []byte:
		return normalizeString(string(x))
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return x
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			return normalizeString(string(b))
		}
		return normalizeJSON(decoded, "")
	}
}

func normalizeString(s string) string {
	trim := strings.TrimSpace(s)
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		var v any
		if json.Unmarshal([]byte(trim), &v) == nil {
			b, _ := marshalStable(normalizeJSON(v, ""))
			return string(b)
		}
	}
	protected := meaningfulStringValues(s)
	for token, value := range protected {
		s = strings.ReplaceAll(s, value, token)
	}
	for _, n := range normalizers {
		s = n.re.ReplaceAllString(s, n.repl)
	}
	for token, value := range protected {
		s = strings.ReplaceAll(s, token, value)
	}
	return s
}

func meaningfulStringValues(s string) map[string]string {
	protected := map[string]string{}
	for _, re := range meaningfulStringPatterns {
		for _, value := range re.FindAllString(s, -1) {
			token := fmt.Sprintf("<meaningful-%d>", len(protected))
			protected[token] = value
		}
	}
	return protected
}

func normalizeJSON(v any, key string) any {
	if forbiddenKeys[key] {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeJSON(val, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeJSON(val, key)
		}
		if isUnorderedKey(key) {
			sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]) < fmt.Sprint(out[j]) })
		}
		return out
	case string:
		return normalizeString(x)
	default:
		return x
	}
}

func isUnorderedKey(key string) bool {
	switch key {
	case "items", "tags", "ids", "names", "routes":
		return true
	}
	return false
}

func marshalStable(v any) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(b.String())), nil
}
