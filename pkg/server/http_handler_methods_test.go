package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/credimi"
	"github.com/forkbombeu/credimi-runner/pkg/gen/mobile"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/stretchr/testify/require"
)

func newStorePipelineMethodService(t *testing.T, responseStatus int, responseBody string) *runnerService {
	t.Helper()

	baseURL := "http://example.local"
	storeURL := baseURL + "/api/wallet/store-pipeline-result"
	client := &fakeHTTPClient{
		handlers: map[string]func(*http.Request) (*http.Response, error){
			http.MethodPost + " " + storeURL: func(req *http.Request) (*http.Response, error) {
				return newResponse(responseStatus, responseBody), nil
			},
		},
	}

	store := &memoryFileStore{}
	writer, err := store.Create("results/run-1/video.mp4")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/last.png")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	writer, err = store.Create("results/run-1/log.txt")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	return NewRunnerServiceWithDeps(NewProcessStore(), map[string]utils.Instance{
		"prod": {URL: baseURL, UserAPIKey: "user-key"},
	}, Deps{
		HTTPClient: client,
		FileStore:  store,
	})
}

func TestStorePipelineResult_MethodResponseShapes(t *testing.T) {
	video := "results/run-1/video.mp4"
	last := "results/run-1/last.png"
	log := "results/run-1/log.txt"
	runnerID := "runner-1"
	baseURL := "http://example.local"
	platform := "android"

	t.Run("empty body returns empty object", func(t *testing.T) {
		srv := newStorePipelineMethodService(t, http.StatusOK, "")
		result, err := srv.StorePipelineResult(context.Background(), &credimi.StorePipelineResultPayload{
			InstanceURL:      baseURL,
			VideoPath:        &video,
			LastFramePath:    &last,
			LogPath:          &log,
			Platform:         platform,
			RunIdentifier:    "run-1",
			RunnerIdentifier: &runnerID,
		})
		require.NoError(t, err)
		require.Equal(t, map[string]any{}, result)
	})

	t.Run("null body returns empty object", func(t *testing.T) {
		srv := newStorePipelineMethodService(t, http.StatusOK, "null")
		result, err := srv.StorePipelineResult(context.Background(), &credimi.StorePipelineResultPayload{
			InstanceURL:      baseURL,
			VideoPath:        &video,
			LastFramePath:    &last,
			LogPath:          &log,
			Platform:         platform,
			RunIdentifier:    "run-1",
			RunnerIdentifier: &runnerID,
		})
		require.NoError(t, err)
		require.Equal(t, map[string]any{}, result)
	})

	t.Run("non-object JSON returns internal error", func(t *testing.T) {
		srv := newStorePipelineMethodService(t, http.StatusOK, "[")
		_, err := srv.StorePipelineResult(context.Background(), &credimi.StorePipelineResultPayload{
			InstanceURL:      baseURL,
			VideoPath:        &video,
			LastFramePath:    &last,
			LogPath:          &log,
			Platform:         platform,
			RunIdentifier:    "run-1",
			RunnerIdentifier: &runnerID,
		})
		require.Error(t, err)
		var svcErr *credimi.APIError
		require.ErrorAs(t, err, &svcErr)
		require.Equal(t, "internal_error", svcErr.Name)
	})
}

func TestTouchFingerprint_MethodError(t *testing.T) {
	srv := NewRunnerServiceWithDeps(NewProcessStore(), nil, Deps{
		CommandRunner: &fakeCommandRunner{
			output: []byte("oops"),
			err:    errors.New("adb boom"),
		},
		Sleeper: func(d time.Duration) {},
	})

	_, err := srv.TouchFingerprint(context.Background())
	require.Error(t, err)
	var svcErr *mobile.APIError
	require.ErrorAs(t, err, &svcErr)
	require.Equal(t, "internal_error", svcErr.Name)
}
