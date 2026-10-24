package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestUpdateRestartPolicy(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.True(t, strings.HasSuffix(r.URL.Path, "/containers/c-new/update"))
				var update container.UpdateConfig
				require.NoError(t, json.NewDecoder(r.Body).Decode(&update))
				assert.Equal(t, container.RestartPolicyMode("always"), update.RestartPolicy.Name)
				w.Header().Set("Content-Type", "application/json")
				if fail {
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"message":"no update"}`))
					return
				}
				_, _ = w.Write([]byte(`{"Warnings":[]}`))
			}))
			defer server.Close()
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
			require.NoError(t, err)
			err = NewRuntimeWithClient(cli).UpdateRestartPolicy(context.Background(), "c-new", domain.RestartPolicyAlways)
			if fail {
				require.ErrorContains(t, err, "restart policy")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
