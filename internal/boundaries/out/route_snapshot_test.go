package out_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

var (
	_ out.RouteSnapshotProvider = (*mocks.MockRouteSnapshotProvider)(nil)
	_ out.RouteSnapshotWatcher  = (*mocks.MockRouteSnapshotWatcher)(nil)
	_ out.EdgeDrainCoordinator  = (*mocks.MockEdgeDrainCoordinator)(nil)
)

func TestRouteSnapshotPorts_ExposeOnlyRoutingContract(t *testing.T) {
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	snapshotType := reflect.TypeOf(domain.RouteTargetSnapshot{})
	generationType := reflect.TypeOf(domain.RouteTargetGeneration(0))
	targetKeyType := reflect.TypeOf(domain.RouteTargetKey(""))
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	assertMethodSignature(t, reflect.TypeOf((*out.RouteSnapshotProvider)(nil)).Elem(), "CurrentSnapshot", []reflect.Type{contextType}, []reflect.Type{snapshotType, errorType})
	assertMethodSignature(t, reflect.TypeOf((*out.RouteSnapshotWatcher)(nil)).Elem(), "WatchSnapshots", []reflect.Type{contextType}, []reflect.Type{reflect.ChanOf(reflect.RecvDir, snapshotType), errorType})
	assertMethodSignature(t, reflect.TypeOf((*out.EdgeDrainCoordinator)(nil)).Elem(), "AcknowledgeDrain", []reflect.Type{contextType, reflect.TypeOf(""), targetKeyType, generationType}, []reflect.Type{errorType})
}

func TestRouteSnapshotPorts_AcceptCancelledContextAndKeepWatcherChannelReceiveOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	provider := mocks.NewMockRouteSnapshotProvider(t)
	provider.EXPECT().CurrentSnapshot(ctx).Return(domain.RouteTargetSnapshot{}, nil)
	_, err := provider.CurrentSnapshot(ctx)
	if err != nil {
		t.Fatalf("get current snapshot: %v", err)
	}

	watcher := mocks.NewMockRouteSnapshotWatcher(t)
	updates := make(chan domain.RouteTargetSnapshot)
	close(updates)
	watcher.EXPECT().WatchSnapshots(ctx).Return((<-chan domain.RouteTargetSnapshot)(updates), nil)
	stream, err := watcher.WatchSnapshots(ctx)
	if err != nil {
		t.Fatalf("watch snapshots: %v", err)
	}
	if stream == nil {
		t.Fatal("watch snapshots returned a nil stream")
	}

	coordinator := mocks.NewMockEdgeDrainCoordinator(t)
	targetKey, err := domain.NewRouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
	if err != nil {
		t.Fatalf("create route target key: %v", err)
	}
	coordinator.EXPECT().AcknowledgeDrain(ctx, "app.example.com", targetKey, domain.RouteTargetGeneration(1)).Return(nil)
	if err := coordinator.AcknowledgeDrain(ctx, "app.example.com", targetKey, 1); err != nil {
		t.Fatalf("acknowledge drain: %v", err)
	}
	if err := ctx.Err(); err == nil {
		t.Fatal("expected cancelled context to remain observable by port implementations")
	}
}

func assertMethodSignature(t *testing.T, port reflect.Type, name string, inputs, outputs []reflect.Type) {
	t.Helper()
	if port.NumMethod() != 1 {
		t.Fatalf("%s exposes %d methods; want exactly one narrow operation", port.Name(), port.NumMethod())
	}

	method, found := port.MethodByName(name)
	if !found {
		t.Fatalf("%s does not expose %s", port.Name(), name)
	}
	if method.Type.NumIn() != len(inputs) || method.Type.NumOut() != len(outputs) {
		t.Fatalf("%s has %d inputs and %d outputs; want %d inputs and %d outputs", name, method.Type.NumIn(), method.Type.NumOut(), len(inputs), len(outputs))
	}
	for index, want := range inputs {
		if got := method.Type.In(index); got != want {
			t.Errorf("%s input %d is %s; want %s", name, index, got, want)
		}
	}
	for index, want := range outputs {
		if got := method.Type.Out(index); got != want {
			t.Errorf("%s output %d is %s; want %s", name, index, got, want)
		}
	}
}
