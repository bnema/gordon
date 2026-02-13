package components

import (
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// TableColumn defines a table column.
type TableColumn struct {
	Title string
	Width int
}

// TableModel is a styled table component.
type TableModel struct {
	columns     []TableColumn
	rows        [][]string
	border      lipgloss.Border
	borderStyle lipgloss.Style
	headerStyle lipgloss.Style
	cellStyle   lipgloss.Style
	oddStyle    lipgloss.Style
	evenStyle   lipgloss.Style
	width       int
}

// TableOption configures a TableModel.
type TableOption func(*TableModel)

// NewTable creates a new styled table.
func NewTable(opts ...TableOption) *TableModel {
	t := &TableModel{
		border:      lipgloss.RoundedBorder(),
		borderStyle: lipgloss.NewStyle().Foreground(styles.ColorBorder),
		headerStyle: lipgloss.NewStyle().
			Bold(true).
			Foreground(styles.ColorPrimary).
			Padding(0, 1),
		cellStyle: lipgloss.NewStyle().
			Foreground(styles.ColorText).
			Padding(0, 1),
		oddStyle: lipgloss.NewStyle().
			Foreground(styles.ColorText).
			Padding(0, 1),
		evenStyle: lipgloss.NewStyle().
			Foreground(styles.ColorText).
			Padding(0, 1),
	}

	for _, opt := range opts {
		opt(t)
	}

	return t
}

// WithColumns sets the table columns.
func WithColumns(cols []TableColumn) TableOption {
	return func(t *TableModel) {
		t.columns = cols
	}
}

// WithRows sets the table rows.
func WithRows(rows [][]string) TableOption {
	return func(t *TableModel) {
		t.rows = rows
	}
}

// WithBorder sets the table border style.
func WithBorder(b lipgloss.Border) TableOption {
	return func(t *TableModel) {
		t.border = b
	}
}

// WithTableWidth sets the table width.
func WithTableWidth(w int) TableOption {
	return func(t *TableModel) {
		t.width = w
	}
}

// WithHeaderStyle sets the header style.
func WithHeaderStyle(s lipgloss.Style) TableOption {
	return func(t *TableModel) {
		t.headerStyle = s
	}
}

// WithCellStyle sets the cell style.
func WithCellStyle(s lipgloss.Style) TableOption {
	return func(t *TableModel) {
		t.cellStyle = s
		t.oddStyle = s
		t.evenStyle = s
	}
}

// WithAlternateRows enables alternating row colors.
func WithAlternateRows(odd, even lipgloss.Style) TableOption {
	return func(t *TableModel) {
		t.oddStyle = odd
		t.evenStyle = even
	}
}

// SetColumns sets the table columns.
func (t *TableModel) SetColumns(cols []TableColumn) {
	t.columns = cols
}

// SetRows sets the table rows.
func (t *TableModel) SetRows(rows [][]string) {
	t.rows = rows
}

// AddRow adds a row to the table.
func (t *TableModel) AddRow(row []string) {
	t.rows = append(t.rows, row)
}

// ClearRows removes all rows from the table.
func (t *TableModel) ClearRows() {
	t.rows = nil
}

// Render renders the table as a string.
func (t *TableModel) Render() string {
	if len(t.columns) == 0 {
		return ""
	}

	// Extract headers
	headers := make([]string, len(t.columns))
	for i, col := range t.columns {
		headers[i] = truncateCell(col.Title, col.Width)
	}

	rows := make([][]string, len(t.rows))
	for rowIdx, row := range t.rows {
		rows[rowIdx] = make([]string, len(row))
		for colIdx, cell := range row {
			width := 0
			if colIdx < len(t.columns) {
				width = t.columns[colIdx].Width
			}
			rows[rowIdx][colIdx] = truncateCell(cell, width)
		}
	}

	// Create table
	tbl := table.New().
		Border(t.border).
		BorderStyle(t.borderStyle).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			width := 0
			if col >= 0 && col < len(t.columns) {
				width = t.columns[col].Width
			}

			applyWidth := func(s lipgloss.Style) lipgloss.Style {
				if width > 0 {
					return s.Width(width).MaxWidth(width)
				}
				return s
			}

			if row == table.HeaderRow {
				return applyWidth(t.headerStyle)
			}
			if row%2 == 0 {
				return applyWidth(t.evenStyle)
			}
			return applyWidth(t.oddStyle)
		})

	// Apply width if set
	if t.width > 0 {
		tbl = tbl.Width(t.width)
	}

	return tbl.String()
}

func truncateCell(value string, maxWidth int) string {
	if strings.Contains(value, "\x1b[") {
		return value
	}

	if maxWidth <= 0 || runewidth.StringWidth(value) <= maxWidth {
		return value
	}

	if maxWidth <= 3 {
		return strings.Repeat(".", maxWidth)
	}

	targetWidth := maxWidth - 3
	b := strings.Builder{}
	currentWidth := 0
	g := uniseg.NewGraphemes(value)
	for g.Next() {
		grapheme := g.Str()
		graphemeWidth := runewidth.StringWidth(grapheme)
		if currentWidth+graphemeWidth > targetWidth {
			break
		}
		b.WriteString(grapheme)
		currentWidth += graphemeWidth
	}

	if b.Len() == 0 {
		return strings.Repeat(".", maxWidth)
	}

	return b.String() + "..."
}

// View returns the table view (alias for Render).
func (t *TableModel) View() string {
	return t.Render()
}

// SimpleTable creates a simple table with headers and rows.
// This is a convenience function for quick table rendering.
func SimpleTable(headers []string, rows [][]string) string {
	cols := make([]TableColumn, len(headers))
	for i, h := range headers {
		cols[i] = TableColumn{Title: h}
	}

	t := NewTable(
		WithColumns(cols),
		WithRows(rows),
	)

	return t.Render()
}

// RouteTable creates a table specifically for displaying routes.
func RouteTable(routes [][]string) string {
	return SimpleTable(
		[]string{"Domain", "Image", "Status"},
		routes,
	)
}

// TokenTable creates a table specifically for displaying tokens.
func TokenTable(tokens [][]string) string {
	return SimpleTable(
		[]string{"ID", "Subject", "Scopes", "Expires", "Revoked"},
		tokens,
	)
}

// SecretTable creates a table specifically for displaying secrets.
func SecretTable(secrets [][]string) string {
	return SimpleTable(
		[]string{"Key", "Value"},
		secrets,
	)
}
