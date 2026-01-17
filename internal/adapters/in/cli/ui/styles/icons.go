package styles

// Nerd Font icons for terminal UI.
// These icons require a Nerd Font compatible terminal font.
// All icons are defined using Unicode escape sequences for portability.
// Fallback ASCII alternatives are provided where practical.
const (
	// Status indicators
	IconSuccess = "\uf00c" // nf-fa-check
	IconError   = "\uf00d" // nf-fa-times
	IconWarning = "\uf071" // nf-fa-exclamation_triangle
	IconInfo    = "\uf05a" // nf-fa-info_circle
	IconPending = "\uf017" // nf-fa-clock

	// Container status icons
	IconRunning    = "\uf00c" // nf-fa-check (green)
	IconStopped    = "\uf04d" // nf-fa-stop
	IconExited     = "\uf00d" // nf-fa-times
	IconPaused     = "\uf04c" // nf-fa-pause
	IconRestarting = "\uf021" // nf-fa-refresh
	IconUnknown    = "\uf071" // nf-fa-exclamation_triangle

	// Objects
	IconRoute     = "\uf0e8"     // nf-fa-sitemap
	IconSecret    = "\uf023"     // nf-fa-lock
	IconToken     = "\uf084"     // nf-fa-key
	IconContainer = "\uf489"     // nf-oct-container (U+F489)
	IconServer    = "\uf233"     // nf-fa-server
	IconNetwork   = "\U000f06f3" // nf-md-lan (U+F06F3)
	IconVolume    = "\uf1c0"     // nf-fa-database
	IconImage     = "\uf187"     // nf-fa-archive
	IconDocker    = "\ue7b0"     // nf-dev-docker

	// Actions
	IconAdd     = "\uf067" // nf-fa-plus
	IconRemove  = "\uf068" // nf-fa-minus
	IconEdit    = "\uf040" // nf-fa-pencil
	IconRefresh = "\uf021" // nf-fa-refresh
	IconUpload  = "\uf0ee" // nf-fa-cloud_upload
	IconDelete  = "\uf1f8" // nf-fa-trash

	// Navigation
	IconArrowRight = "\uf061" // nf-fa-arrow_right
	IconArrowLeft  = "\uf060" // nf-fa-arrow_left
	IconArrowUp    = "\uf062" // nf-fa-arrow_up
	IconArrowDown  = "\uf063" // nf-fa-arrow_down
	IconChevron    = "\uf054" // nf-fa-chevron_right

	// UI elements
	IconBullet     = "\u25b8" // ▸ Simple triangle bullet
	IconDot        = "\u25cf" // ● Filled circle
	IconDotEmpty   = "\u25cb" // ○ Empty circle
	IconCheckbox   = "\uf096" // nf-fa-square_o
	IconChecked    = "\uf046" // nf-fa-check_square_o
	IconRadio      = "\uf10c" // nf-fa-circle_o
	IconRadioCheck = "\uf192" // nf-fa-dot_circle_o
	IconPlay       = "\uf04b" // nf-fa-play
	IconStop       = "\uf04d" // nf-fa-stop
	IconPause      = "\uf04c" // nf-fa-pause

	// Tree structure
	IconTreeBranch = "\u251c" // ├
	IconTreeLast   = "\u2514" // └
	IconTreeLine   = "\u2500" // ─
	IconTreeVert   = "\u2502" // │

	// Spinners (individual frames)
	SpinnerDot    = "\u28fe\u28fd\u28fb\u28bf\u287f\u28df\u28ef\u28f7"
	SpinnerLine   = "|/-\\"
	SpinnerCircle = "\u25d0\u25d3\u25d1\u25d2"
	SpinnerBrail  = "\u280b\u2819\u2839\u2838\u283c\u2834\u2826\u2827\u2807\u280f"

	// Borders and decorations
	BorderHorizontal = "\u2500" // ─
	BorderVertical   = "\u2502" // │
	BorderCornerTL   = "\u250c" // ┌
	BorderCornerTR   = "\u2510" // ┐
	BorderCornerBL   = "\u2514" // └
	BorderCornerBR   = "\u2518" // ┘
	BorderT          = "\u252c" // ┬
	BorderB          = "\u2534" // ┴
	BorderL          = "\u251c" // ├
	BorderR          = "\u2524" // ┤
	BorderCross      = "\u253c" // ┼

	// Gordon branding
	IconGordon = "\uf1b2" // nf-fa-cube
)

// ASCII fallback alternatives for terminals without Nerd Fonts.
const (
	AsciiSuccess    = "[OK]"
	AsciiError      = "[X]"
	AsciiWarning    = "[!]"
	AsciiInfo       = "[i]"
	AsciiBullet     = ">"
	AsciiDot        = "*"
	AsciiRunning    = "[R]"
	AsciiStopped    = "[S]"
	AsciiTreeBranch = "|-"
	AsciiTreeLast   = "`-"
)
