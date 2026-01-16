package styles

import "github.com/charmbracelet/lipgloss"

// Theme contains all composed styles for the Gordon TUI.
// Styles are organized by component type for easy discovery.
var Theme = struct {
	// Text styles
	Title     lipgloss.Style
	Subtitle  lipgloss.Style
	Heading   lipgloss.Style
	Body      lipgloss.Style
	Muted     lipgloss.Style
	Bold      lipgloss.Style
	Highlight lipgloss.Style

	// Status styles
	Success lipgloss.Style
	Error   lipgloss.Style
	Warning lipgloss.Style
	Info    lipgloss.Style

	// Badge styles (compact status indicators)
	BadgeSuccess lipgloss.Style
	BadgeError   lipgloss.Style
	BadgeWarning lipgloss.Style
	BadgeInfo    lipgloss.Style
	BadgePending lipgloss.Style

	// Table styles
	TableHeader      lipgloss.Style
	TableRow         lipgloss.Style
	TableRowSelected lipgloss.Style
	TableRowAlt      lipgloss.Style
	TableCell        lipgloss.Style
	TableBorder      lipgloss.Style

	// Form styles
	FormLabel       lipgloss.Style
	FormInput       lipgloss.Style
	FormPlaceholder lipgloss.Style
	FormFocused     lipgloss.Style
	FormError       lipgloss.Style

	// List styles
	ListItem         lipgloss.Style
	ListItemSelected lipgloss.Style
	ListBullet       lipgloss.Style

	// Box/Container styles
	Box          lipgloss.Style
	BoxHighlight lipgloss.Style
	BoxError     lipgloss.Style

	// Banner/Header styles
	Banner      lipgloss.Style
	BannerTitle lipgloss.Style

	// Help styles
	HelpKey  lipgloss.Style
	HelpDesc lipgloss.Style
}{
	// Text styles
	Title: lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary),

	Subtitle: lipgloss.NewStyle().
		Foreground(ColorTextMuted),

	Heading: lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText),

	Body: lipgloss.NewStyle().
		Foreground(ColorText),

	Muted: lipgloss.NewStyle().
		Foreground(ColorTextMuted),

	Bold: lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText),

	Highlight: lipgloss.NewStyle().
		Foreground(ColorPrimary),

	// Status styles
	Success: lipgloss.NewStyle().
		Foreground(ColorSuccess),

	Error: lipgloss.NewStyle().
		Foreground(ColorError),

	Warning: lipgloss.NewStyle().
		Foreground(ColorWarning),

	Info: lipgloss.NewStyle().
		Foreground(ColorInfo),

	// Badge styles
	BadgeSuccess: lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorSuccess).
		Padding(0, 1),

	BadgeError: lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorError).
		Padding(0, 1),

	BadgeWarning: lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorWarning).
		Padding(0, 1),

	BadgeInfo: lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorInfo).
		Padding(0, 1),

	BadgePending: lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorTextMuted).
		Padding(0, 1),

	// Table styles
	TableHeader: lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary).
		BorderBottom(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(ColorBorder),

	TableRow: lipgloss.NewStyle().
		Foreground(ColorText),

	TableRowSelected: lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Background(ColorBgMuted),

	TableRowAlt: lipgloss.NewStyle().
		Foreground(ColorTextMuted),

	TableCell: lipgloss.NewStyle().
		Padding(0, 1),

	TableBorder: lipgloss.NewStyle().
		Foreground(ColorBorder),

	// Form styles
	FormLabel: lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true),

	FormInput: lipgloss.NewStyle().
		Foreground(ColorText).
		Background(ColorBgMuted).
		Padding(0, 1),

	FormPlaceholder: lipgloss.NewStyle().
		Foreground(ColorTextMuted),

	FormFocused: lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Background(ColorBgMuted).
		BorderLeft(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 1),

	FormError: lipgloss.NewStyle().
		Foreground(ColorError),

	// List styles
	ListItem: lipgloss.NewStyle().
		Foreground(ColorText).
		PaddingLeft(2),

	ListItemSelected: lipgloss.NewStyle().
		Foreground(ColorPrimary).
		PaddingLeft(2),

	ListBullet: lipgloss.NewStyle().
		Foreground(ColorPrimary),

	// Box/Container styles
	Box: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 2),

	BoxHighlight: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2),

	BoxError: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorError).
		Padding(1, 2),

	// Banner/Header styles
	Banner: lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPrimary).
		Padding(0, 2),

	BannerTitle: lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary),

	// Help styles
	HelpKey: lipgloss.NewStyle().
		Foreground(ColorPrimary),

	HelpDesc: lipgloss.NewStyle().
		Foreground(ColorTextMuted),
}

// Render helpers for common patterns.

// RenderStatus returns a styled status indicator with icon.
func RenderStatus(status string, running bool) string {
	if running {
		return Theme.Success.Render(IconSuccess + " " + status)
	}
	return Theme.Error.Render(IconError + " " + status)
}

// RenderBadge returns a styled badge for the given status.
func RenderBadge(status string) string {
	switch status {
	case "running", "success", "ok", "active":
		return Theme.BadgeSuccess.Render(status)
	case "error", "failed", "stopped":
		return Theme.BadgeError.Render(status)
	case "warning", "degraded":
		return Theme.BadgeWarning.Render(status)
	case "pending", "starting":
		return Theme.BadgePending.Render(status)
	default:
		return Theme.BadgeInfo.Render(status)
	}
}

// RenderKeyHelp returns formatted key binding help text.
func RenderKeyHelp(key, desc string) string {
	return Theme.HelpKey.Render(key) + " " + Theme.HelpDesc.Render(desc)
}

// RenderListItem returns a formatted list item with bullet.
func RenderListItem(item string, selected bool) string {
	bullet := Theme.ListBullet.Render(IconBullet)
	if selected {
		return bullet + " " + Theme.ListItemSelected.Render(item)
	}
	return bullet + " " + Theme.ListItem.Render(item)
}

// RenderError returns a styled error message.
func RenderError(msg string) string {
	return Theme.Error.Render(IconError + " " + msg)
}

// RenderSuccess returns a styled success message.
func RenderSuccess(msg string) string {
	return Theme.Success.Render(IconSuccess + " " + msg)
}

// RenderWarning returns a styled warning message.
func RenderWarning(msg string) string {
	return Theme.Warning.Render(IconWarning + " " + msg)
}

// RenderInfo returns a styled info message.
func RenderInfo(msg string) string {
	return Theme.Info.Render(IconInfo + " " + msg)
}
