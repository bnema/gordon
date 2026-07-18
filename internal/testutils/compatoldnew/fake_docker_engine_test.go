package compatoldnew

// This fixture deliberately speaks the Docker HTTP protocol on a private Unix
// socket. It never implements a Gordon port: production runtime wiring, gRPC,
// authentication, and container use cases remain in the child process.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fakeDockerDeployment struct{ domain, image string }

type fakeDockerEngine struct {
	t      *testing.T
	socket string
	server *http.Server
	ln     net.Listener
	mu     sync.Mutex
	calls  []string
	deploy []fakeDockerDeployment
	nextID int
	peers  map[int]struct{}
}

func newFakeDockerEngine(t *testing.T, socket string) *fakeDockerEngine {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDockerEngine{t: t, socket: socket, ln: ln, peers: make(map[int]struct{})}
	f.server = &http.Server{Handler: http.HandlerFunc(f.serveHTTP)}
	go func() { _ = f.server.Serve(fakeDockerListener{Listener: ln, engine: f}) }()
	return f
}

func (f *fakeDockerEngine) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p := dockerAPIPath(r.URL.Path)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+p)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && p == "/_ping":
		w.Header().Set("API-Version", "1.44")
		_, _ = w.Write([]byte("OK"))
	case r.Method == http.MethodGet && p == "/version":
		_, _ = w.Write([]byte(`{"Version":"25.0.0","ApiVersion":"1.44","MinAPIVersion":"1.24","Os":"linux","Arch":"amd64"}`))
	case r.Method == http.MethodGet && p == "/containers/json":
		_, _ = w.Write([]byte(`[]`))
	case r.Method == http.MethodGet && p == "/images/json":
		_, _ = w.Write([]byte(`[]`))
	case r.Method == http.MethodPost && p == "/images/create":
		// Docker's pull endpoint is a JSON stream. EOF after a successful HTTP
		// response is accepted by the production client and keeps this fixture
		// limited to the operation actually requested.
		_, _ = w.Write([]byte(`{"status":"Downloaded"}`))
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/tag"):
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodGet && p == "/networks":
		_, _ = w.Write([]byte(`[]`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/networks/"):
		http.Error(w, `{"message":"network not found"}`, http.StatusNotFound)
	case r.Method == http.MethodPost && p == "/networks/create":
		_, _ = w.Write([]byte(`{"Id":"gordon-net"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		_, _ = w.Write([]byte(`{"Id":"sha256:fixture","Config":{"ExposedPorts":{"8080/tcp":{}},"Labels":{},"Env":[]}}`))
	case r.Method == http.MethodPost && p == "/containers/create":
		var request struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, `{"message":"invalid create payload"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("fixture-%d", f.nextID)
		f.deploy = append(f.deploy, fakeDockerDeployment{domain: request.Labels["gordon.domain"], image: request.Image})
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"Id":%q}`, id)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		id := path.Base(path.Dir(p))
		_, _ = fmt.Fprintf(w, `{"Id":%q,"Name":"/gordon-fixture","Image":"sha256:fixture","Created":%q,"Config":{"Image":"split/app:v1","Labels":{}},"State":{"Status":"running","ExitCode":0},"NetworkSettings":{"Ports":{}}}`, id, time.Now().UTC().Format(time.RFC3339Nano))
	default:
		http.Error(w, fmt.Sprintf(`{"message":"unsupported fixture request %s %s"}`, r.Method, p), http.StatusNotFound)
	}
}

func dockerAPIPath(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) > 0 && strings.HasPrefix(parts[0], "v1.") {
		return "/" + strings.Join(parts[1:], "/")
	}
	return p
}

type fakeDockerListener struct {
	net.Listener
	engine *fakeDockerEngine
}

func (l fakeDockerListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		raw, rawErr := unixConn.SyscallConn()
		if rawErr == nil {
			_ = raw.Control(func(fd uintptr) {
				cred, credErr := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
				if credErr == nil {
					l.engine.mu.Lock()
					l.engine.peers[int(cred.Pid)] = struct{}{}
					l.engine.mu.Unlock()
				}
			})
		}
	}
	return conn, nil
}

// sawProcess proves the child opened this exact listener: Unix peer credentials
// are kernel-supplied and cannot be supplied by Docker protocol request data.
func (f *fakeDockerEngine) sawProcess(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.peers[pid]
	return ok
}

func (f *fakeDockerEngine) deploymentCount(domain, image string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, deploy := range f.deploy {
		if deploy.domain == domain && deploy.image == image {
			count++
		}
	}
	return count
}
func (f *fakeDockerEngine) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.deploy) }

// assertDeploymentProtocol keeps the fixture narrow: a real deployment must
// traverse the adapter's pull, network, inspect, create, and start requests.
func (f *fakeDockerEngine) assertDeploymentProtocol(t *testing.T, wantCreates int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := make(map[string]int, len(f.calls))
	for _, call := range f.calls {
		seen[call]++
	}
	for _, required := range []string{"POST /images/create", "POST /networks/create", "POST /containers/create"} {
		if seen[required] == 0 {
			t.Fatalf("production Docker adapter did not issue required %s request", required)
		}
	}
	if seen["POST /containers/create"] != wantCreates {
		t.Fatalf("production Docker adapter create count: got %d want %d", seen["POST /containers/create"], wantCreates)
	}
}

func (f *fakeDockerEngine) close() {
	_ = f.server.Close()
	_ = f.ln.Close()
	_ = os.Remove(f.socket)
}
