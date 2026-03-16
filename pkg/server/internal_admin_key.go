package server

import (
	"net/http"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

const internalAdminKeyHeader = "Credimi-Api-Key"

func setInternalAdminKeyHeader(req *http.Request) {
	internalAdminKey := utils.GetEnvironmentVariable("CREDIMI_INTERNAL_ADMIN_KEY")
	if internalAdminKey == "" {
		return
	}
	req.Header.Set(internalAdminKeyHeader, internalAdminKey)
}
