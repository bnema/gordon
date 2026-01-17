// Package styles provides a unified styling system for Gordon's TUI.
// The color palette is inspired by the Gordon webapp's terminal-themed design.
package styles

import "github.com/charmbracelet/lipgloss"

// Color definitions using the Gordon design system.
// These colors are adapted from the webapp CSS for terminal use.
var (
	// Primary colors - Terminal Green
	Green50  = lipgloss.Color("#e8fdf4")
	Green100 = lipgloss.Color("#c5fae4")
	Green200 = lipgloss.Color("#8ef5c8")
	Green300 = lipgloss.Color("#4dead7")
	Green400 = lipgloss.Color("#1adf9a")
	Green500 = lipgloss.Color("#00cc6a")
	Green600 = lipgloss.Color("#00995a")
	Green700 = lipgloss.Color("#007a4a")
	Green800 = lipgloss.Color("#005c38")
	Green900 = lipgloss.Color("#003d25")

	// Secondary colors - Cyan
	Cyan50  = lipgloss.Color("#e6f9ff")
	Cyan100 = lipgloss.Color("#c2f0ff")
	Cyan200 = lipgloss.Color("#85e0ff")
	Cyan300 = lipgloss.Color("#47d1ff")
	Cyan400 = lipgloss.Color("#1ac5ff")
	Cyan500 = lipgloss.Color("#00a0cc")
	Cyan600 = lipgloss.Color("#0088b3")
	Cyan700 = lipgloss.Color("#006d8f")
	Cyan800 = lipgloss.Color("#00526c")
	Cyan900 = lipgloss.Color("#003748")

	// Accent colors - Violet
	Violet50  = lipgloss.Color("#f5f3ff")
	Violet100 = lipgloss.Color("#ede9fe")
	Violet200 = lipgloss.Color("#ddd6fe")
	Violet300 = lipgloss.Color("#c4b5fd")
	Violet400 = lipgloss.Color("#a78bfa")
	Violet500 = lipgloss.Color("#8b5cf6")
	Violet600 = lipgloss.Color("#7c3aed")
	Violet700 = lipgloss.Color("#6d28d9")
	Violet800 = lipgloss.Color("#5b21b6")
	Violet900 = lipgloss.Color("#4c1d95")

	// Neutral colors - for text and backgrounds
	Neutral50  = lipgloss.Color("#fafafa")
	Neutral100 = lipgloss.Color("#f5f5f5")
	Neutral200 = lipgloss.Color("#e5e5e5")
	Neutral300 = lipgloss.Color("#d4d4d4")
	Neutral400 = lipgloss.Color("#a3a3a3")
	Neutral500 = lipgloss.Color("#737373")
	Neutral600 = lipgloss.Color("#525252")
	Neutral700 = lipgloss.Color("#404040")
	Neutral800 = lipgloss.Color("#262626")
	Neutral900 = lipgloss.Color("#171717")
	Neutral950 = lipgloss.Color("#0a0a0a")

	// Terminal neon colors (for dark terminal backgrounds)
	NeonGreen  = lipgloss.Color("#00ff88")
	NeonCyan   = lipgloss.Color("#00ccff")
	NeonViolet = lipgloss.Color("#a78bfa")
	NeonRed    = lipgloss.Color("#ff4444")
	NeonYellow = lipgloss.Color("#fbbf24")

	// Semantic colors
	ColorPrimary   = NeonGreen
	ColorSecondary = NeonCyan
	ColorAccent    = NeonViolet
	ColorSuccess   = NeonGreen
	ColorWarning   = NeonYellow
	ColorError     = NeonRed
	ColorInfo      = NeonCyan

	// Text colors
	ColorText       = Neutral200
	ColorTextMuted  = Neutral500
	ColorTextBright = lipgloss.Color("#ffffff")

	// Background colors
	ColorBg        = lipgloss.Color("#000000")
	ColorBgSurface = Neutral900
	ColorBgMuted   = Neutral800

	// Border colors
	ColorBorder      = Neutral700
	ColorBorderMuted = Neutral800
)
