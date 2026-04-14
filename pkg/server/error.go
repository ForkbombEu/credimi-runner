package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/forkbombeu/credimi-runner/pkg/gen/credimi"
	"github.com/forkbombeu/credimi-runner/pkg/gen/docs"
	"github.com/forkbombeu/credimi-runner/pkg/gen/health"
	"github.com/forkbombeu/credimi-runner/pkg/gen/mobile"
	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/gen/worker"

	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

func normalizeAPIError(apiErr *runner.APIError) *runner.APIError {
	if apiErr == nil {
		return &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "InternalError",
			Message: "internal server error",
		}
	}
	if apiErr.Code == 0 {
		apiErr.Code = http.StatusInternalServerError
	}
	return apiErr
}

func wrapWorkerAPIError(apiErr *runner.APIError) error {
	apiErr = normalizeAPIError(apiErr)
	if apiErr.Code == http.StatusBadRequest {
		return &worker.APIError{
			Name:    "bad_request",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	}
	return &worker.APIError{
		Name:    "internal_error",
		Code:    apiErr.Code,
		Domain:  apiErr.Domain,
		Reason:  apiErr.Reason,
		Message: apiErr.Message,
	}
}

func wrapCredimiAPIError(apiErr *runner.APIError) error {
	apiErr = normalizeAPIError(apiErr)

	switch apiErr.Code {
	case http.StatusBadRequest:
		return &credimi.APIError{
			Name:    "bad_request",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	case http.StatusUnauthorized:
		return &credimi.APIError{
			Name:    "unauthorized",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	case http.StatusForbidden:
		return &credimi.APIError{
			Name:    "forbidden",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	case http.StatusBadGateway:
		return &credimi.APIError{
			Name:    "bad_gateway",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	default:
		return &credimi.APIError{
			Name:    "internal_error",
			Code:    apiErr.Code,
			Domain:  apiErr.Domain,
			Reason:  apiErr.Reason,
			Message: apiErr.Message,
		}
	}
}

func wrapMobileAPIError(apiErr *runner.APIError) error {
	apiErr = normalizeAPIError(apiErr)
	return &mobile.APIError{
		Name:    "internal_error",
		Code:    apiErr.Code,
		Domain:  apiErr.Domain,
		Reason:  apiErr.Reason,
		Message: apiErr.Message,
	}
}

func wrapDocsAPIError(apiErr *docs.APIError) error {
	if apiErr == nil {
		return &docs.APIError{
			Name:    "internal_error",
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "internal error",
			Message: "internal server error",
		}
	}
	if apiErr.Code == 0 {
		apiErr.Code = http.StatusInternalServerError
	}
	if apiErr.Name == "" {
		apiErr.Name = "internal_error"
	}
	return apiErr
}

// Implements goahttp.Statuser and matches your API error JSON.
type apiErrorWire struct {
	Name    string `json:"name"`
	Status  int    `json:"status"`
	Domain  string `json:"error"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

var _ goahttp.Statuser = (*apiErrorWire)(nil)

func (e *apiErrorWire) StatusCode() int { return e.Status }

func wireFromrunnerAPIError(e *runner.APIError) *apiErrorWire {
	if e == nil {
		return &apiErrorWire{
			Name:    "internal_error",
			Status:  http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "internal error",
			Message: "internal server error",
		}
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

func wireFromworkerAPIError(e *worker.APIError) *apiErrorWire {
	if e == nil {
		return wireFromrunnerAPIError(nil)
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

func wireFromcredimiAPIError(e *credimi.APIError) *apiErrorWire {
	if e == nil {
		return wireFromrunnerAPIError(nil)
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

func wireFrommobileAPIError(e *mobile.APIError) *apiErrorWire {
	if e == nil {
		return wireFromrunnerAPIError(nil)
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

func wireFromdocsAPIError(e *docs.APIError) *apiErrorWire {
	if e == nil {
		return wireFromrunnerAPIError(nil)
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

func wireFromhealthAPIError(e *health.APIError) *apiErrorWire {
	if e == nil {
		return wireFromrunnerAPIError(nil)
	}
	status := e.Code
	if status == 0 {
		status = http.StatusInternalServerError
	}
	name := e.Name
	if name == "" {
		name = "internal_error"
	}
	return &apiErrorWire{
		Name:    name,
		Status:  status,
		Domain:  e.Domain,
		Reason:  e.Reason,
		Message: e.Message,
	}
}

// GoaErrorFormatter is what you pass as the LAST argument to server.New(...).
func GoaErrorFormatter(_ context.Context, err error) goahttp.Statuser {
	// If the error is (or wraps) *runner.APIError, encode it as your wire error.
	var apiErr *runner.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return wireFromrunnerAPIError(apiErr)
	}

	var workerAPIErr *worker.APIError
	if errors.As(err, &workerAPIErr) && workerAPIErr != nil {
		return wireFromworkerAPIError(workerAPIErr)
	}

	var credimiAPIErr *credimi.APIError
	if errors.As(err, &credimiAPIErr) && credimiAPIErr != nil {
		return wireFromcredimiAPIError(credimiAPIErr)
	}

	var mobileAPIErr *mobile.APIError
	if errors.As(err, &mobileAPIErr) && mobileAPIErr != nil {
		return wireFrommobileAPIError(mobileAPIErr)
	}

	var docsAPIErr *docs.APIError
	if errors.As(err, &docsAPIErr) && docsAPIErr != nil {
		return wireFromdocsAPIError(docsAPIErr)
	}

	var healthAPIErr *health.APIError
	if errors.As(err, &healthAPIErr) && healthAPIErr != nil {
		return wireFromhealthAPIError(healthAPIErr)
	}

	// If this is a goa.ServiceError, try to unwrap the original error.
	var svcErr *goa.ServiceError
	if errors.As(err, &svcErr) && svcErr != nil {
		// goa.ServiceError in your version stores the wrapped error in an unexported field,
		// but it typically supports errors.Unwrap.
		if u := errors.Unwrap(svcErr); u != nil {
			if errors.As(u, &apiErr) && apiErr != nil {
				return wireFromrunnerAPIError(apiErr)
			}
		}

		// Map decode errors into your API error shape (optional).
		switch svcErr.Name {
		case goa.DecodePayload, goa.MissingPayload:
			return &apiErrorWire{
				Name:    "bad_request",
				Status:  http.StatusBadRequest,
				Domain:  "server",
				Reason:  "invalid JSON",
				Message: "invalid request body",
			}
		}

		// Fallback if you want: map known Goa error names → HTTP codes
		switch svcErr.Name {
		case "bad_request":
			return &apiErrorWire{Name: "bad_request", Status: 400, Domain: "server", Reason: "bad request", Message: svcErr.Error()}
		case "unauthorized":
			return &apiErrorWire{Name: "unauthorized", Status: 401, Domain: "server", Reason: "unauthorized", Message: svcErr.Error()}
		case "bad_gateway":
			return &apiErrorWire{Name: "bad_gateway", Status: 502, Domain: "server", Reason: "bad gateway", Message: svcErr.Error()}
		default:
			return &apiErrorWire{Name: "internal_error", Status: 500, Domain: "server", Reason: "internal error", Message: svcErr.Error()}
		}
	}

	// Final fallback
	return &apiErrorWire{
		Name:    "internal_error",
		Status:  http.StatusInternalServerError,
		Domain:  "server",
		Reason:  "internal error",
		Message: "internal server error",
	}
}
