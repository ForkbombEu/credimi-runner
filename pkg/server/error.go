package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"

	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

func wrapAPIError(apiErr *runner.APIError) error {
	if apiErr == nil {
		return runner.MakeInternalError(&runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "InternalError",
			Message: "internal server error",
		})
	}

	switch apiErr.Code {
	case http.StatusBadRequest:
		return runner.MakeBadRequest(apiErr)
	case http.StatusUnauthorized:
		return runner.MakeUnauthorized(apiErr)
	case http.StatusBadGateway:
		return runner.MakeBadGateway(apiErr)
	default:
		// everything else treated as internal error
		if apiErr.Code == 0 {
			apiErr.Code = http.StatusInternalServerError
		}
		return runner.MakeInternalError(apiErr)
	}
}

// Implements goahttp.Statuser and matches your API error JSON.
type apiErrorWire struct {
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
	return &apiErrorWire{
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
				Status:  http.StatusBadRequest,
				Domain:  "server",
				Reason:  "invalid JSON",
				Message: "invalid request body",
			}
		}

		// Fallback if you want: map known Goa error names → HTTP codes
		switch svcErr.Name {
		case "bad_request":
			return &apiErrorWire{Status: 400, Domain: "server", Reason: "bad request", Message: svcErr.Error()}
		case "unauthorized":
			return &apiErrorWire{Status: 401, Domain: "server", Reason: "unauthorized", Message: svcErr.Error()}
		case "bad_gateway":
			return &apiErrorWire{Status: 502, Domain: "server", Reason: "bad gateway", Message: svcErr.Error()}
		default:
			return &apiErrorWire{Status: 500, Domain: "server", Reason: "internal error", Message: svcErr.Error()}
		}
	}

	// Final fallback
	return &apiErrorWire{
		Status:  http.StatusInternalServerError,
		Domain:  "server",
		Reason:  "internal error",
		Message: "internal server error",
	}
}
