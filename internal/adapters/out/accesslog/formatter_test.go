package accesslog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testTime = time.Date(2026, 4, 3, 14, 20, 54, 167_000_000, time.UTC)

var baseEntry = Entry{
	Time:       testTime,
	ClientIP:   "135.181.213.219",
	Method:     "GET",
	Host:       "traefik.example.dev",
	Path:       "/robots.txt",
	Query:      "",
	Status:     404,
	BytesSent:  19,
	DurationMS: 0.167,
	UserAgent:  "MJ12bot/v1.4.8",
	Referer:    "",
	RequestID:  "95126efcb65d4df81f7ae633e2a712cd",
	Proto:      "HTTP/1.1",
}

// --- JSON formatter ---

func TestFormatJSON_AllFields(t *testing.T) {
	line, err := formatJSON(baseEntry)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &got))

	assert.Equal(t, "2026-04-03T14:20:54.167Z", got["time"])
	assert.Equal(t, "135.181.213.219", got["client_ip"])
	assert.Equal(t, "GET", got["method"])
	assert.Equal(t, "traefik.example.dev", got["host"])
	assert.Equal(t, "/robots.txt", got["path"])
	assert.Equal(t, "", got["query"])
	assert.InDelta(t, 404, got["status"], 0)
	assert.InDelta(t, 19, got["bytes_sent"], 0)
	assert.InDelta(t, 0.167, got["duration_ms"], 0.0001)
	assert.Equal(t, "MJ12bot/v1.4.8", got["user_agent"])
	assert.Equal(t, "", got["referer"])
	assert.Equal(t, "95126efcb65d4df81f7ae633e2a712cd", got["request_id"])
	assert.Equal(t, "HTTP/1.1", got["proto"])
}

func TestFormatJSON_OneLinePerEntry(t *testing.T) {
	line, err := formatJSON(baseEntry)
	require.NoError(t, err)
	assert.False(t, strings.Contains(line, "\n"), "JSON line must not contain newline")
}

func TestFormatJSON_TimestampMillisecondPrecision(t *testing.T) {
	// Ensure we get .167Z and not RFC3339Nano (which would be .167000000Z)
	line, err := formatJSON(baseEntry)
	require.NoError(t, err)
	assert.Contains(t, line, `"2026-04-03T14:20:54.167Z"`)
}

func TestFormatJSON_ClientIPNoPort(t *testing.T) {
	e := baseEntry
	e.ClientIP = "10.0.0.1" // ensure no port
	line, err := formatJSON(e)
	require.NoError(t, err)
	assert.Contains(t, line, `"client_ip":"10.0.0.1"`)
}

func TestFormatJSON_EmptyOptionalFields(t *testing.T) {
	e := baseEntry
	e.Query = ""
	e.Referer = ""
	e.UserAgent = ""
	line, err := formatJSON(e)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &got))
	assert.Equal(t, "", got["query"])
	assert.Equal(t, "", got["referer"])
	assert.Equal(t, "", got["user_agent"])
}

func TestFormatJSON_WithQuery(t *testing.T) {
	e := baseEntry
	e.Query = "foo=bar&baz=qux"
	line, err := formatJSON(e)
	require.NoError(t, err)
	assert.Contains(t, line, `"query":"foo=bar\u0026baz=qux"`)
}

// --- CLF formatter ---

func TestFormatCLF_BasicLine(t *testing.T) {
	line, err := formatCLF(baseEntry)
	require.NoError(t, err)
	assert.Equal(t, `135.181.213.219 - - [03/Apr/2026:14:20:54 +0000] "GET /robots.txt HTTP/1.1" 404 19`, line)
}

func TestFormatCLF_WithQuery(t *testing.T) {
	e := baseEntry
	e.Query = "page=2"
	line, err := formatCLF(e)
	require.NoError(t, err)
	assert.Contains(t, line, `"GET /robots.txt?page=2 HTTP/1.1"`)
}

func TestFormatCLF_NoRefererOrUA(t *testing.T) {
	// CLF format should not include referer or UA
	line, err := formatCLF(baseEntry)
	require.NoError(t, err)
	assert.NotContains(t, line, "MJ12bot")
}

func TestFormatCLF_EscapesQuotesInTarget(t *testing.T) {
	e := baseEntry
	e.Path = `/path/"quoted"`
	line, err := formatCLF(e)
	require.NoError(t, err)
	// Forward slashes do not need escaping; only backslashes and double-quotes do.
	assert.Contains(t, line, `/path/\"quoted\"`)
}

// --- Combined formatter ---

func TestFormatCombined_BasicLine(t *testing.T) {
	line, err := formatCombined(baseEntry)
	require.NoError(t, err)
	assert.Equal(t,
		`135.181.213.219 - - [03/Apr/2026:14:20:54 +0000] "GET /robots.txt HTTP/1.1" 404 19 "-" "MJ12bot/v1.4.8"`,
		line,
	)
}

func TestFormatCombined_EmptyRefererIsDash(t *testing.T) {
	e := baseEntry
	e.Referer = ""
	line, err := formatCombined(e)
	require.NoError(t, err)
	assert.Contains(t, line, `"-"`)
}

func TestFormatCombined_EmptyUAIsDash(t *testing.T) {
	e := baseEntry
	e.UserAgent = ""
	line, err := formatCombined(e)
	require.NoError(t, err)
	// last quoted field should be "-"
	assert.True(t, strings.HasSuffix(line, `"-"`))
}

func TestFormatCombined_WithReferer(t *testing.T) {
	e := baseEntry
	e.Referer = "https://example.com/"
	line, err := formatCombined(e)
	require.NoError(t, err)
	assert.Contains(t, line, `"https://example.com/"`)
}

func TestFormatCombined_EscapesQuotesInUA(t *testing.T) {
	e := baseEntry
	e.UserAgent = `bot/"evil"`
	line, err := formatCombined(e)
	require.NoError(t, err)
	// Forward slashes don't need escaping; only backslashes and double-quotes do.
	assert.Contains(t, line, `"bot/\"evil\""`)
}

// --- buildRequestTarget ---

func TestBuildRequestTarget_NoQuery(t *testing.T) {
	assert.Equal(t, "/foo", buildRequestTarget("/foo", ""))
}

func TestBuildRequestTarget_WithQuery(t *testing.T) {
	assert.Equal(t, "/foo?bar=1", buildRequestTarget("/foo", "bar=1"))
}

// --- escapeQuoted ---

func TestEscapeQuoted(t *testing.T) {
	assert.Equal(t, `hello`, escapeQuoted(`hello`))
	assert.Equal(t, `say \"hi\"`, escapeQuoted(`say "hi"`))
	assert.Equal(t, `back\\slash`, escapeQuoted(`back\slash`))
	assert.Equal(t, `\"\\\"`, escapeQuoted(`"\"`))
}
