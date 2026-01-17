package styles

// Nerd Font icons for terminal UI.
// These icons require a Nerd Font compatible terminal font.
// Fallback ASCII alternatives are provided where practical.
const (
	// Status indicators
	IconSuccess = "" // nf-fa-check (U+F00C)
	IconError   = "" // nf-fa-times (U+F00D)
	IconWarning = "" // nf-fa-exclamation_triangle (U+F071)
	IconInfo    = "" // nf-fa-info_circle (U+F05A)
	IconPending = "" // nf-fa-clock_o (U+F017)

	// Objects
	IconRoute     = ""  // nf-fa-sitemap (U+F0E8)
	IconSecret    = ""  // nf-fa-lock (U+F023)
	IconToken     = ""  // nf-fa-key (U+F084)
	IconContainer = ""  // nf-oct-container (U+F489)
	IconServer    = ""  // nf-fa-server (U+F233)
	IconNetwork   = "󰛳" // nf-md-lan (U+F06F3)
	IconVolume    = ""  // nf-fa-database (U+F1C0)
	IconImage     = ""  // nf-fa-archive (U+F187)

	// Actions
	IconAdd     = "" // nf-fa-plus (U+F067)
	IconRemove  = "" // nf-fa-minus (U+F068)
	IconEdit    = "" // nf-fa-pencil (U+F040)
	IconRefresh = "" // nf-fa-refresh (U+F021)
	IconUpload  = "" // nf-fa-cloud_upload (U+F0EE)
	IconDelete  = "" // nf-fa-trash (U+F1F8)

	// Navigation
	IconArrowRight = "" // nf-fa-arrow_right (U+F061)
	IconArrowLeft  = "" // nf-fa-arrow_left (U+F060)
	IconArrowUp    = "" // nf-fa-arrow_up (U+F062)
	IconArrowDown  = "" // nf-fa-arrow_down (U+F063)
	IconChevron    = "" // nf-fa-chevron_right (U+F054)

	// UI elements
	IconBullet     = "▸" // Simple triangle bullet
	IconDot        = "●" // Filled circle
	IconDotEmpty   = "○" // Empty circle
	IconCheckbox   = ""  // nf-fa-square_o (U+F096)
	IconChecked    = ""  // nf-fa-check_square_o (U+F046)
	IconRadio      = ""  // nf-fa-circle_o (U+F10C)
	IconRadioCheck = ""  // nf-fa-dot_circle_o (U+F192)

	// Spinners (individual frames)
	SpinnerDot    = "⣾⣽⣻⢿⡿⣟⣯⣷"
	SpinnerLine   = "|/-\\"
	SpinnerCircle = "◐◓◑◒"
	SpinnerBrail  = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

	// Borders and decorations
	BorderHorizontal = "─"
	BorderVertical   = "│"
	BorderCornerTL   = "┌"
	BorderCornerTR   = "┐"
	BorderCornerBL   = "└"
	BorderCornerBR   = "┘"
	BorderT          = "┬"
	BorderB          = "┴"
	BorderL          = "├"
	BorderR          = "┤"
	BorderCross      = "┼"

	// Gordon branding
	IconGordon = "" // nf-fa-cube (U+F1B2)
)

// ASCII fallback alternatives for terminals without Nerd Fonts.
const (
	AsciiSuccess = "[OK]"
	AsciiError   = "[X]"
	AsciiWarning = "[!]"
	AsciiInfo    = "[i]"
	AsciiBullet  = ">"
	AsciiDot     = "*"
)
