package deployment

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestDeployHandover(t *testing.T) {
	for _, failure := range []string{"", "prepare", "hold", "inhibit", "retire", "start", "ready", "policy"} {
		t.Run(failure, func(t *testing.T) {
			state := outmocks.NewMockAppState(t)
			runtime := outmocks.NewMockContainerRuntime(t)
			traffic := outmocks.NewMockAppTrafficRefresher(t)
			var events []string
			add := func(e string) { events = append(events, e) }
			p := pinnedService{name: "web", spec: domain.AppService{Name: "web", Volumes: []domain.AppVolume{{Name: "data", Path: "/data"}}}}
			if failure == "ready" {
				p.spec.Readiness.Type = "invalid"
			}
			state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-1"}, nil)
			state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
			runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil)
			runtime.EXPECT().CreateNetwork(mock.Anything, mock.Anything, mock.Anything).Return(nil)
			runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil)
			runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, cfg *domain.ContainerConfig) (*domain.Container, error) {
				add("create")
				assert.Empty(t, cfg.RestartPolicy)
				if failure == "prepare" {
					return nil, assert.AnError
				}
				return &domain.Container{ID: "new"}, nil
			}).Once()
			if failure != "prepare" {
				traffic.EXPECT().HoldService(mock.Anything, "blog", "web", mock.Anything).RunAndReturn(func(_ context.Context, _, _ string, until time.Time) error {
					add("hold")
					assert.WithinDuration(t, time.Now().Add(55*time.Second), until, time.Second)
					if failure == "hold" {
						return assert.AnError
					}
					return nil
				}).Once()
				if failure != "hold" {
					state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).RunAndReturn(func(context.Context, domain.AppRecoveryInhibition) error {
						add("inhibit")
						if failure == "inhibit" {
							return assert.AnError
						}
						return nil
					}).Once()
					if failure != "inhibit" {
						runtime.EXPECT().StopContainer(mock.Anything, "old", mock.Anything).RunAndReturn(func(context.Context, string, time.Duration) error { add("stop-old"); return nil }).Once()
						runtime.EXPECT().RemoveContainer(mock.Anything, "old", false).RunAndReturn(func(context.Context, string, bool) error {
							add("remove-old")
							if failure == "retire" {
								return assert.AnError
							}
							return nil
						}).Once()
						if failure != "retire" {
							state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "old").Return(nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "new").RunAndReturn(func(context.Context, string) error {
								add("start")
								if failure == "start" {
									return assert.AnError
								}
								return nil
							}).Once()
							if failure != "start" {
								runtime.EXPECT().UpdateRestartPolicy(mock.Anything, "new", domain.RestartPolicyAlways).RunAndReturn(func(context.Context, string, string) error {
									add("policy")
									if failure == "policy" {
										return assert.AnError
									}
									return nil
								}).Once()
								runtime.EXPECT().InspectContainer(mock.Anything, "new").Return(&domain.Container{ID: "new", StartedAt: time.Now()}, nil).Once()
								if failure == "ready" {
									runtime.EXPECT().GetContainerLogs(mock.Anything, "new", false).Return(io.NopCloser(strings.NewReader("diagnostic\n")), nil).Once()
								}
							}
						}
					}
					if failure != "" && failure != "policy" {
						traffic.EXPECT().WithdrawService(mock.Anything, "blog", "web").RunAndReturn(func(context.Context, string, string) error { add("release"); return nil }).Once()
					}
				}
				if failure != "" && failure != "policy" {
					runtime.EXPECT().RemoveContainer(mock.Anything, "new", true).RunAndReturn(func(context.Context, string, bool) error { add("remove-new"); return nil }).Once()
					state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "new").Return(nil).Once()
				}
			}
			svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic, Handover: HandoverConfig{MaxHold: time.Minute}}, zerowrap.Default())
			result := svc.deployService(context.Background(), "blog", "rev-1", p, "op-1", "old", 10*time.Second, nil)
			if failure == "" || failure == "policy" {
				require.Equal(t, "deployed", result.Result)
				assert.Equal(t, []string{"create", "hold", "inhibit", "stop-old", "remove-old", "start", "policy"}, events)
				if failure == "policy" {
					require.Len(t, result.CleanupWarnings, 1)
					assert.Contains(t, result.CleanupWarnings[0].Detail, "native restart policy not applied")
				}
			} else {
				require.Equal(t, "failed", result.Result)
				if failure == "prepare" {
					assert.Equal(t, []string{"create"}, events)
				}
				if failure == "hold" {
					assert.Equal(t, []string{"create", "hold", "remove-new"}, events)
					assert.True(t, svc.publication.inhibited("blog", "web"))
				}
				if failure == "ready" {
					assert.Equal(t, []string{"diagnostic"}, result.Diagnostics)
				}
			}
		})
	}
}

func TestHandoverBudget(t *testing.T) {
	svc := NewService(Deps{Handover: HandoverConfig{MaxHold: time.Minute}}, zerowrap.Default())
	spec := domain.AppService{Readiness: domain.AppReadiness{Timeout: 10 * time.Second}}
	assert.Equal(t, 30*time.Second, svc.handoverBudget(spec, 5*time.Second))
	assert.Equal(t, time.Minute, svc.handoverBudget(spec, time.Minute))
}

func TestRestartHandoverRuntimeFailureReleasesHold(t *testing.T) {
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	traffic := outmocks.NewMockAppTrafficRefresher(t)
	var events []string
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	traffic.EXPECT().HoldService(mock.Anything, "blog", "web", mock.Anything).RunAndReturn(func(context.Context, string, string, time.Time) error { events = append(events, "hold"); return nil }).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "old", mock.Anything).RunAndReturn(func(context.Context, string, time.Duration) error {
		events = append(events, "restart")
		return assert.AnError
	}).Once()
	traffic.EXPECT().WithdrawService(mock.Anything, "blog", "web").RunAndReturn(func(context.Context, string, string) error { events = append(events, "release"); return nil }).Once()
	svc := NewService(Deps{State: state, Runtime: runtime, Traffic: traffic, Handover: HandoverConfig{MaxHold: time.Minute}}, zerowrap.Default())
	op := domain.AppOperation{Steps: []domain.AppOperationStep{{}}}
	result := svc.restartOneService(context.Background(), "blog", "op-1", "web", domain.AppEffectiveService{Container: "old", Spec: domain.AppService{Name: "web"}}, &op, 0)
	assert.Equal(t, "failed", result.Result)
	assert.Equal(t, []string{"hold", "restart", "release"}, events)
}
