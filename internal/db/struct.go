package db

type User struct {
	ID      int64
	Name    string
	Email   string
	Account Account
}

type Account struct {
	ID        int64
	UserID    int64
	Sessions  []Session
	Providers []Provider
}

type Session struct {
	ID           int64
	AccountID    int64
	SessionToken string
	Expires      string
	IsOnline     bool
}

type Provider struct {
	ID           int64
	AccountID    int64
	Name         string
	AccessToken  string
	RefreshToken string
	Expires      string
}
