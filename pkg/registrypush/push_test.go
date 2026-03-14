package registrypush_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/pkg/registrypush"
)

func TestNew_defaults(t *testing.T) {
	p := registrypush.New()
	assert.NotNil(t, p)
}

func TestNew_withChunkSize(t *testing.T) {
	p := registrypush.New(registrypush.WithChunkSize(10 * 1024 * 1024))
	assert.NotNil(t, p)
}
