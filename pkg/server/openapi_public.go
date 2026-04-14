package server

import (
	"encoding/json"
	"os"
	"strings"

	docs "github.com/forkbombeu/credimi-runner/pkg/gen/docs"
)

func readOpenAPI3PublicContent() ([]byte, error) {
	content, err := readOpenAPI3PublicJSON()
	if err != nil {
		return nil, internalDocsAssetError("read openapi3 public json", err)
	}

	serverURL, ok := resolvePublicServerURL()
	if !ok {
		return content, nil
	}

	var spec map[string]any
	if err := json.Unmarshal(content, &spec); err != nil {
		return nil, internalDocsAssetError("decode openapi3 public json", err)
	}

	spec["servers"] = []any{
		map[string]any{"url": serverURL},
	}

	body, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, internalDocsAssetError("encode openapi3 public json", err)
	}

	return append(body, '\n'), nil
}

func internalDocsAssetError(reason string, err error) *docs.APIError {
	return &docs.APIError{
		Name:    "internal_error",
		Code:    500,
		Domain:  "server",
		Reason:  reason,
		Message: err.Error(),
	}
}

func resolvePublicServerURL() (string, bool) {
	domain := strings.TrimSpace(os.Getenv("RUNNER_DOMAIN"))
	if domain == "" || domain == ":80" {
		return "", false
	}

	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		return domain, true
	}

	return "https://" + domain, true
}
