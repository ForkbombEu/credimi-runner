package server

import (
	"context"
	"encoding/json"
	"net/http"

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

func (s *runnerService) FetchInstallerAndAction(ctx context.Context, payload *credimi.FetchInstallerAndActionPayload) (*credimi.Fetchinstallerandactionresult, error) {
	var body fetchInstallerAndActionPayload
	if payload.InstanceURL != "" {
		body.InstanceURL = payload.InstanceURL
	}
	if payload.VersionIdentifier != "" {
		body.VersionIdentifier = payload.VersionIdentifier
	}
	if payload.ActionIdentifier != nil {
		body.ActionIdentifier = *payload.ActionIdentifier
	}
	if payload.Platform != "" {
		body.Platform = payload.Platform
	}
	if payload.SkipInstaller != nil {
		body.SkipInstaller = *payload.SkipInstaller
	}

	result, apiErr := s.fetchInstallerAndActionLogic(body)
	if apiErr != nil {
		return nil, wrapCredimiAPIError(apiErr)
	}

	return &credimi.Fetchinstallerandactionresult{
		InstallerPath: result.InstallerPath,
		VersionID:     result.VersionID,
		Code:          result.Code,
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
	if payload.LogPath != nil {
		body.LogPath = *payload.LogPath
	}
	if payload.RunIdentifier != "" {
		body.RunIdentifier = payload.RunIdentifier
	}
	if payload.RunnerIdentifier != nil {
		body.RunnerIdentifier = *payload.RunnerIdentifier
	}
	if payload.Platform != "" {
		body.Platform = payload.Platform
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
