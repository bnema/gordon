package server

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/db"
	"github.com/charmbracelet/log"
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
	DBSaveCtx       context.Context    // Context for the auto-save routine
	DBSaveCancel    context.CancelFunc // Cancel function for the auto-save routine
	StartTime       time.Time
}

type DBTables struct {
	User        db.User        `sql:"user"`
	Account     db.Account     `sql:"account"`
	Sessions    db.Sessions    `sql:"sessions"`
	Provider    db.Provider    `sql:"provider"`
	Clients     db.Clients     `sql:"clients"`
	Domain      db.Domain      `sql:"domain"`
	Certificate db.Certificate `sql:"certificate"`
	ProxyRoute  db.ProxyRoute  `sql:"proxy_route"`
	AcmeAccount db.AcmeAccount `sql:"acme_account"`
}

// GetConfig returns the configuration
func (a *App) GetConfig() *common.Config {
	return &a.Config
}

// GetDB returns the database connection
func (a *App) GetDB() *sql.DB {
	return a.DB
}

// GenerateOauthCallbackURL generates the OAuth callback URL
func (a *App) GenerateOauthCallbackURL() string {
	var scheme, port string
	config := a.Config

	if config.Http.Https {
		scheme = "https"
		port = ""
	} else {
		scheme = "http"
		port = fmt.Sprintf(":%s", a.Config.Http.Port)
	}

	domain := config.Http.Domain
	if config.Http.SubDomain != "" {
		domain = fmt.Sprintf("%s.%s", config.Http.SubDomain, config.Http.Domain)
	}

	callbackURL := fmt.Sprintf("%s://%s%s/callback", scheme, domain, port)
	log.Debug("Generated OAuth Callback URL", "url", callbackURL)
	return callbackURL
}

func (a *App) IsDevEnvironment() bool {
	return a.Config.Build.RunEnv == "dev"
}

func (a *App) GetUptime() string {
	uptime := time.Since(a.StartTime)
	return uptime.String()
}

// GetVersion returns the version of the app
func (a *App) GetVersionstring() string {
	return fmt.Sprint(a.Config.Build.BuildVersion)
}

// Shutdown performs a clean shutdown of the application
func (a *App) Shutdown() error {
	log.Info("Initiating application shutdown")

	// Close the database connection
	if err := CloseDB(a); err != nil {
		log.Error("Error during database shutdown", "error", err)
		return err
	}

	log.Info("Application shutdown completed successfully")
	return nil
}
