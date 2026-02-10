package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	runnerhttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/runner/server"
	runner "github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"goa.design/clue/debug"
	cluelog "goa.design/clue/log"
	goahttp "goa.design/goa/v3/http"
)

type slashNormalizedFS struct {
	fs http.FileSystem
}

func (s slashNormalizedFS) Open(name string) (http.File, error) {
	return s.fs.Open(strings.TrimPrefix(name, "/"))
}

func projectRootDirForFS() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func NewHTTPHandler(ctx context.Context, rs *runnerService, dbg bool) http.Handler {

	endpoints := runner.NewEndpoints(rs)
	endpoints.Use(debug.LogPayloads())
	endpoints.Use(cluelog.Endpoint)

	// Goa mux + optional debug mounts
	mux := goahttp.NewMuxer()
	if dbg {
		debug.MountPprofHandlers(debug.Adapt(mux))
		debug.MountDebugLogEnabler(debug.Adapt(mux))
	}

	staticFS := slashNormalizedFS{fs: http.Dir(projectRootDirForFS())}

	// Transport server
	srv := runnerhttp.New(
		endpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
		staticFS,
	)
	runnerhttp.Mount(mux, srv)

	// HTTP middleware stack
	var handler http.Handler = mux
	if dbg {
		handler = debug.HTTP()(handler)
	}
	handler = cluelog.HTTP(ctx)(handler)

	// Log mounts (super useful)
	for _, m := range srv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}

	return handler
}

func (s *runnerService) ProcessStartMissing(ctx context.Context) error {
	return runner.MakeBadRequest(&runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "Server",
		Reason:  "NamespaceMissing",
		Message: "namespace is required",
	})
}

func (s *runnerService) ProcessStart(ctx context.Context, payload *runner.ProcessStartPayload) (*runner.Processstartresult, error) {
	oldNamespace := ""
	if payload.OldNamespace != nil {
		oldNamespace = *payload.OldNamespace
	}

	result, apiErr := s.processStart(payload.Namespace, oldNamespace)
	if apiErr != nil {
		return nil, wrapAPIError(apiErr)
	}

	return &runner.Processstartresult{Status: result.Status, Namespace: result.Namespace}, nil
}

func (s *runnerService) ProcessList(ctx context.Context) ([]string, error) {
	return s.processList(), nil
}

func (s *runnerService) FetchApkAndAction(ctx context.Context, payload *runner.FetchApkAndActionPayload) (*runner.Fetchapkandactionresult, error) {
	var body fetchApkAndActionPayload
	if payload.InstanceURL != "" {
		body.InstanceURL = payload.InstanceURL
	}
	if payload.VersionIdentifier != "" {
		body.VersionIdentifier = payload.VersionIdentifier
	}
	if payload.ActionIdentifier != nil {
		body.ActionIdentifier = *payload.ActionIdentifier
	}

	result, apiErr := s.fetchApkAndActionLogic(body)
	if apiErr != nil {
		return nil, wrapAPIError(apiErr)
	}

	return &runner.Fetchapkandactionresult{
		ApkPath:   result.ApkPath,
		VersionID: result.VersionID,
		Code:      result.Code,
	}, nil
}

func (s *runnerService) StorePipelineResult(ctx context.Context, payload *runner.StorePipelineResultPayload) (any, error) {
	var body storePipelineResultPayload
	if payload.InstanceURL != "" {
		body.InstanceURL = payload.InstanceURL
	}
	if payload.VideoPath != nil {
		body.VideoPath = *payload.VideoPath
	}
	if payload.LastFramePath != nil {
		body.LastFramePath = *payload.LastFramePath
	}
	if payload.LogcatPath != nil {
		body.LogcatPath = *payload.LogcatPath
	}
	if payload.RunIdentifier != "" {
		body.RunIdentifier = payload.RunIdentifier
	}
	if payload.RunnerIdentifier != nil {
		body.RunnerIdentifier = *payload.RunnerIdentifier
	}
	result, apiErr := s.storePipelineResultLogic(body)
	if apiErr != nil {
		return nil, wrapAPIError(apiErr)
	}

	return json.RawMessage(result), nil
}

func (s *runnerService) TouchFingerprint(ctx context.Context) (*runner.Touchfingerprintresult, error) {
	result, apiErr := s.touchFingerprintLogic()
	if apiErr != nil {
		return nil, wrapAPIError(apiErr)
	}

	return &runner.Touchfingerprintresult{Status: result.Status}, nil
}
