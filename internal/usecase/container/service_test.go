package container

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_ListNetworks(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{NetworkPrefix: "gordon"})
	ctx := testContext()

	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: "gordon-app", Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "bridge"},
		{Name: "gordon-shared", Labels: map[string]string{domain.LabelManaged: "true"}},
		{Name: "gordon-unmanaged"},
	}, nil)

	networks, err := svc.ListNetworks(ctx)

	assert.NoError(t, err)
	assert.Len(t, networks, 2)
	assert.Equal(t, "gordon-app", networks[0].Name)
	assert.Equal(t, "gordon-shared", networks[1].Name)
	assert.NotContains(t, []string{networks[0].Name, networks[1].Name}, "gordon-unmanaged")
}
