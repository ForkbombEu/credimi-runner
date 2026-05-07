package server

import (
	"net/http"
)

const internalAdminKeyHeader = "Credimi-Api-Key"

func setAPIKeyHeader(req *http.Request, apiKey string) {
	if apiKey == "" {
		return
	}
	req.Header.Set(internalAdminKeyHeader, apiKey)
}
