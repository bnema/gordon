package db

type User struct {
	ID      string `sql:"id, primary_key"`
	Name    string `sql:"name"`
	Email   string `sql:"email"`
	Account Account
}

type Account struct {
	ID        string `sql:"id, primary_key"`
	UserID    string `sql:"user_id, foreign_key=user.id"`
	Sessions  []Sessions
	Providers []Provider
}

type Sessions struct {
	ID          string `sql:"id, primary_key"`
	AccountID   string `sql:"account_id, foreign_key=account.id"`
	BrowserInfo string `sql:"browser_info"`
	AccessToken string `sql:"access_token"`
	Expires     string `sql:"expires"`
	IsOnline    bool   `sql:"is_online"`
}

type Provider struct {
	ID         string `sql:"id, primary_key"`
	AccountID  string `sql:"account_id, foreign_key=account.id"`
	Name       string `sql:"name"`
	Login      string `sql:"login"`
	AvatarURL  string `sql:"avatar_url"`
	ProfileURL string `sql:"profile_url"`
	Email      string `sql:"email"`
}
