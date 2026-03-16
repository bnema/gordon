package proxy

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestResolveTargetMetadata_PortAndProtocolFromLabels(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "grpc-app:latest").Return(map[string]string{
		domain.LabelProxyPort:     "50051",
		domain.LabelProxyProtocol: "h2c",
	}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "grpc-app:latest")

	assert.NoError(t, err)
	assert.Equal(t, 50051, meta.Port)
	assert.Equal(t, "h2c", meta.Protocol)
}

func TestResolveTargetMetadata_DefaultProtocolWhenAbsent(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "web:latest").Return(map[string]string{
		domain.LabelProxyPort: "8080",
	}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "web:latest")

	assert.NoError(t, err)
	assert.Equal(t, 8080, meta.Port)
	assert.Equal(t, "", meta.Protocol)
}

func TestResolveTargetMetadata_DeprecatedPortLabel(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "old:latest").Return(map[string]string{
		domain.LabelPort: "3000",
	}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "old:latest")

	assert.NoError(t, err)
	assert.Equal(t, 3000, meta.Port)
	assert.Equal(t, "", meta.Protocol)
}

func TestResolveTargetMetadata_ProxyPortWinsOverDeprecated(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "dual:latest").Return(map[string]string{
		domain.LabelProxyPort: "9000",
		domain.LabelPort:      "3000",
	}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "dual:latest")

	assert.NoError(t, err)
	assert.Equal(t, 9000, meta.Port)
}

func TestResolveTargetMetadata_FallsBackToExposedPort(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "plain:latest").Return(map[string]string{}, nil)
	runtime.EXPECT().GetImageExposedPorts(ctx, "plain:latest").Return([]int{8080}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "plain:latest")

	assert.NoError(t, err)
	assert.Equal(t, 8080, meta.Port)
	assert.Equal(t, "", meta.Protocol)
}

func TestResolveTargetMetadata_UnknownProtocolIgnored(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "exotic:latest").Return(map[string]string{
		domain.LabelProxyPort:     "8080",
		domain.LabelProxyProtocol: "quic",
	}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "exotic:latest")

	assert.NoError(t, err)
	assert.Equal(t, 8080, meta.Port)
	assert.Equal(t, "", meta.Protocol)
}

func TestResolveTargetMetadata_NoPortsAvailable(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "empty:latest").Return(map[string]string{}, nil)
	runtime.EXPECT().GetImageExposedPorts(ctx, "empty:latest").Return([]int{}, nil)

	_, err := svc.resolveTargetMetadata(ctx, "empty:latest")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no exposed ports")
}

func TestResolveTargetMetadata_LabelErrorFallsBackToExposedPort(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := &Service{runtime: runtime}
	ctx := testContext()

	runtime.EXPECT().GetImageLabels(ctx, "broken:latest").Return(nil, fmt.Errorf("image inspect failed"))
	runtime.EXPECT().GetImageExposedPorts(ctx, "broken:latest").Return([]int{3000}, nil)

	meta, err := svc.resolveTargetMetadata(ctx, "broken:latest")

	assert.NoError(t, err)
	assert.Equal(t, 3000, meta.Port)
	assert.Equal(t, "", meta.Protocol)
}
