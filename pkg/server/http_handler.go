package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	credimi "github.com/forkbombeu/credimi-runner/pkg/gen/credimi"
	docs "github.com/forkbombeu/credimi-runner/pkg/gen/docs"
	"github.com/forkbombeu/credimi-runner/pkg/gen/health"
	credimihttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/credimi/server"
	docshttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/docs/server"
	healthhttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/health/server"
	mobilehttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/mobile/server"
	workerhttp "github.com/forkbombeu/credimi-runner/pkg/gen/http/worker/server"
	mobile "github.com/forkbombeu/credimi-runner/pkg/gen/mobile"
	runner "github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	worker "github.com/forkbombeu/credimi-runner/pkg/gen/worker"
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
	healthService := NewHealthService()

	workerEndpoints := worker.NewEndpoints(rs)
	workerEndpoints.Use(debug.LogPayloads())
	workerEndpoints.Use(cluelog.Endpoint)

	credimiEndpoints := credimi.NewEndpoints(rs)
	credimiEndpoints.Use(debug.LogPayloads())
	credimiEndpoints.Use(cluelog.Endpoint)

	mobileEndpoints := mobile.NewEndpoints(rs)
	mobileEndpoints.Use(debug.LogPayloads())
	mobileEndpoints.Use(cluelog.Endpoint)

	docsEndpoints := docs.NewEndpoints(rs)
	docsEndpoints.Use(debug.LogPayloads())
	docsEndpoints.Use(cluelog.Endpoint)

	healthEndpoints := health.NewEndpoints(healthService)
	healthEndpoints.Use(debug.LogPayloads())
	healthEndpoints.Use(cluelog.Endpoint)

	// Goa mux + optional debug mounts
	mux := goahttp.NewMuxer()
	if dbg {
		debug.MountPprofHandlers(debug.Adapt(mux))
		debug.MountDebugLogEnabler(debug.Adapt(mux))
	}

	staticFS := slashNormalizedFS{fs: http.Dir(projectRootDirForFS())}

	workerSrv := workerhttp.New(
		workerEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
	)
	credimiSrv := credimihttp.New(
		credimiEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
	)
	mobileSrv := mobilehttp.New(
		mobileEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
	)
	docsSrv := docshttp.New(
		docsEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
		staticFS,
		staticFS,
		staticFS,
		staticFS,
	)
	healthSrv := healthhttp.New(
		healthEndpoints,
		mux,
		goahttp.RequestDecoder,
		goahttp.ResponseEncoder,
		nil,
		GoaErrorFormatter,
	)
	workerhttp.Mount(mux, workerSrv)
	credimihttp.Mount(mux, credimiSrv)
	mobilehttp.Mount(mux, mobileSrv)
	docshttp.Mount(mux, docsSrv)
	healthhttp.Mount(mux, healthSrv)

	// HTTP middleware stack
	var handler http.Handler = mux
	handler = withPublicOpenAPIServerURL(handler)
	if dbg {
		handler = debug.HTTP()(handler)
	}
	handler = cluelog.HTTP(ctx)(withCORS(handler))

	// Log mounts (super useful)
	for _, m := range workerSrv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range credimiSrv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range mobileSrv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range docsSrv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range healthSrv.Mounts {
		cluelog.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}

	return handler
}

func (s *runnerService) ProcessStart(ctx context.Context, payload *worker.ProcessStartPayload) (*worker.Processstartresult, error) {
	oldNamespace := ""
	if payload.OldNamespace != nil {
		oldNamespace = *payload.OldNamespace
	}

	result, apiErr := s.processStart(payload.Namespace, oldNamespace)
	if apiErr != nil {
		return nil, wrapWorkerAPIError(apiErr)
	}

	return &worker.Processstartresult{Status: result.Status, Namespace: result.Namespace}, nil
}

func (s *runnerService) ProcessList(ctx context.Context) ([]string, error) {
	return s.processList(), nil
}

func (s *runnerService) FetchApkAndAction(ctx context.Context, payload *credimi.FetchApkAndActionPayload) (*credimi.Fetchapkandactionresult, error) {
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

	result, apiErr := s.fetchApkAndActionLogic(ctx, body)
	if apiErr != nil {
		return nil, wrapCredimiAPIError(apiErr)
	}

	return &credimi.Fetchapkandactionresult{
		ApkPath:   result.ApkPath,
		VersionID: result.VersionID,
		Code:      result.Code,
	}, nil
}

func (s *runnerService) StorePipelineResult(ctx context.Context, payload *credimi.StorePipelineResultPayload) (map[string]any, error) {
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
		return nil, wrapCredimiAPIError(apiErr)
	}

	if len(result) == 0 {
		return map[string]any{}, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, wrapCredimiAPIError(&runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "invalid upstream response",
			Message: "store pipeline result returned a non-object JSON body",
		})
	}
	if decoded == nil {
		return map[string]any{}, nil
	}

	return decoded, nil
}

func (s *runnerService) TouchFingerprint(ctx context.Context) (*mobile.Touchfingerprintresult, error) {
	result, apiErr := s.touchFingerprintLogic()
	if apiErr != nil {
		return nil, wrapMobileAPIError(apiErr)
	}

	return &mobile.Touchfingerprintresult{Status: result.Status}, nil
}
