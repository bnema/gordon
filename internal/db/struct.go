package db

type User struct {
	ID      int64  `sql:"id, primary_key"`
	Name    string `sql:"name"`
	Email   string `sql:"email"`
	Account Account
}

type Account struct {
	ID        int64 `sql:"id, primary_key"`
	UserID    int64 `sql:"user_id, foreign_key=user.id"`
	Sessions  []Sessions
	Providers []Provider
}

type Sessions struct {
	ID          int64  `sql:"id, primary_key"`
	AccountID   int64  `sql:"account_id, foreign_key=account.id"`
	BrowserInfo string `sql:"browser_info"`
	Expires     string `sql:"expires"`
	IsOnline    bool   `sql:"is_online"`
}

type Provider struct {
	ID           int64  `sql:"id, primary_key"`
	AccountID    int64  `sql:"account_id, foreign_key=account.id"`
	Name         string `sql:"name"`
	AccessToken  string `sql:"access_token"`
	RefreshToken string `sql:"refresh_token"`
	Expires      string `sql:"expires"`
}
