package server

import (
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/db"
	_ "github.com/joho/godotenv/autoload"
)

type App struct {
	TemplateFS      fs.FS
	PublicFS        fs.FS
	LocYML          []byte // strings.yml contains the strings for the current language
	DBDir           string
	DBFilename      string
	DBPath          string
	InitialChecksum string
	Config          common.Config
	DB              *sql.DB
	DBTables        DBTables
	StartTime       time.Time
}

type DBTables struct {
	User     db.User     `sql:"user"`
	Account  db.Account  `sql:"account"`
	Sessions db.Sessions `sql:"sessions"`
	Provider db.Provider `sql:"provider"`
}

// GenerateOauthCallbackURL generates the OAuth callback URL
func (a *App) GenerateOauthCallbackURL() string {
	var scheme, port string
	config := a.Config
	if config.General.RunEnv == "dev" {
		scheme = "http"
		port = ":" + config.Http.Port
	} else { // Assuming "prod"
		scheme = "https"
		port = ""
	}

	domain := config.Http.TopDomain
	if config.Http.SubDomain != "" {
		domain = fmt.Sprintf("%s.%s", config.Http.SubDomain, config.Http.TopDomain)
	}

	return fmt.Sprintf("%s://%s%s%s/login/oauth/callback", scheme, domain, port, config.Admin.Path)
}

func (a *App) IsDevEnvironment() bool {
	return a.Config.General.RunEnv == "dev"
}

func (a *App) GetUptime() string {
	uptime := time.Since(a.StartTime)
	return uptime.String()
}

// GetVersion returns the version of the app
func (a *App) GetVersionstring() string {
	return fmt.Sprint(a.Config.General.BuildVersion)
}
