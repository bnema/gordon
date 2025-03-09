package interfaces

import (
	"database/sql"

	"github.com/bnema/gordon/internal/common"
)

// AppInterface defines the interface that the proxy package needs from the App struct
type AppInterface interface {
	// Configuration access
	GetConfig() *common.Config

	// Database access
	GetDB() *sql.DB

	// Environment and status
	IsDevEnvironment() bool
	GetUptime() string
	GetVersionstring() string
}
