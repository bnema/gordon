package acmelego

import (
	"crypto"

	"github.com/go-acme/lego/v4/registration"
)

// AccountUser implements the lego registration.User interface for ACME
// account registration and certificate operations.
type AccountUser struct {
	email        string
	privateKey   crypto.PrivateKey
	registration *registration.Resource
}

// NewAccountUser creates a new AccountUser.
func NewAccountUser(email string, key crypto.PrivateKey, reg *registration.Resource) *AccountUser {
	return &AccountUser{
		email:        email,
		privateKey:   key,
		registration: reg,
	}
}

// GetEmail returns the account email.
func (u *AccountUser) GetEmail() string {
	return u.email
}

// GetRegistration returns the ACME registration resource.
func (u *AccountUser) GetRegistration() *registration.Resource {
	return u.registration
}

// GetPrivateKey returns the account private key.
func (u *AccountUser) GetPrivateKey() crypto.PrivateKey {
	return u.privateKey
}

// compile-time interface check
var _ registration.User = (*AccountUser)(nil)
