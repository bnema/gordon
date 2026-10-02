package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
)

func TestCAInfoProvider_DisabledReturnsUntypedNil(t *testing.T) {
	si := &serviceInit{svc: &services{}}

	// A typed-nil *pki.CA wrapped in the interface would be non-nil and make
	// the admin handler dereference a nil CA instead of answering 404.
	assert.True(t, si.caInfoProvider() == nil)
}

func TestCAInfoProvider_EnabledReturnsCA(t *testing.T) {
	si := &serviceInit{svc: &services{caAdapter: &pkiadapter.CA{}}}

	assert.True(t, si.caInfoProvider() != nil)
}
