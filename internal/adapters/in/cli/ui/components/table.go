package components

import (
	"gordon/internal/adapters/in/cli/ui/styles"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
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
		headers[i] = col.Title
	}

	// Create table
	tbl := table.New().
		Border(t.border).
		BorderStyle(t.borderStyle).
		Headers(headers...).
		Rows(t.rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return t.headerStyle
			}
			if row%2 == 0 {
				return t.evenStyle
			}
			return t.oddStyle
		})

	// Apply width if set
	if t.width > 0 {
		tbl = tbl.Width(t.width)
	}

	return tbl.String()
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
