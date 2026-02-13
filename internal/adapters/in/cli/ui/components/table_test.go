package components

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableRender_AppliesConfiguredColumnWidth(t *testing.T) {
	tableModel := NewTable(
		WithColumns([]TableColumn{{Title: "ID", Width: 5}}),
		WithRows([][]string{{"abc"}}),
		WithHeaderStyle(lipgloss.NewStyle()),
		WithCellStyle(lipgloss.NewStyle()),
	)

	rendered := stripANSI(tableModel.Render())
	lines := strings.Split(rendered, "\n")

	var rowLine string
	for _, line := range lines {
		if strings.Contains(line, "abc") {
			rowLine = line
			break
		}
	}

	require.NotEmpty(t, rowLine)
	assert.Contains(t, rowLine, "abc  ")
}

func TestTableRender_TruncatesLongCellTextWithEllipsis(t *testing.T) {
	tableModel := NewTable(
		WithColumns([]TableColumn{{Title: "ID", Width: 5}}),
		WithRows([][]string{{"abcdef"}}),
		WithHeaderStyle(lipgloss.NewStyle()),
		WithCellStyle(lipgloss.NewStyle()),
	)

	rendered := stripANSI(tableModel.Render())
	assert.Contains(t, rendered, "ab...")
	assert.NotContains(t, rendered, "abcdef")
}

func TestTruncateCell_LeavesShortTextUnchanged(t *testing.T) {
	assert.Equal(t, "abc", truncateCell("abc", 5))
}

func TestTruncateCell_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxWidth int
		expected string
	}{
		{name: "zero width passthrough", value: "abcdef", maxWidth: 0, expected: "abcdef"},
		{name: "width three all dots", value: "abcdef", maxWidth: 3, expected: "..."},
		{name: "ascii truncates", value: "abcdef", maxWidth: 5, expected: "ab..."},
		{name: "cjk truncates by display width", value: "你好世界", maxWidth: 5, expected: "你..."},
		{name: "grapheme-safe emoji truncation", value: "👨‍👩‍👧‍👦abcd", maxWidth: 5, expected: "👨‍👩‍👧‍👦..."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := truncateCell(tt.value, tt.maxWidth)
			assert.Equal(t, tt.expected, got)
			if tt.maxWidth > 0 {
				assert.LessOrEqual(t, runewidth.StringWidth(got), tt.maxWidth)
			}
		})
	}
}

func TestTruncateCell_AnsiInputPassthrough(t *testing.T) {
	styled := "\x1b[32mactive\x1b[0m"
	got := truncateCell(styled, 3)
	assert.Equal(t, styled, got)
	assert.Contains(t, got, "\x1b[")
}

func stripANSI(input string) string {
	ansiPattern := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	return ansiPattern.ReplaceAllString(input, "")
}
