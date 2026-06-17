package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/forkbombeu/credimi-runner/pkg/gen/credimi"
	"github.com/forkbombeu/credimi-runner/pkg/gen/mobile"
	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/gen/worker"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	goa "goa.design/goa/v3/pkg"
	"goa.design/goa/v3/security"
)

const adminAPIKeyCacheTTL = 60 * time.Second

func (s *runnerService) APIKeyAuth(ctx context.Context, key string, schema *security.APIKeyScheme) (context.Context, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return ctx, authError(ctx, http.StatusUnauthorized, "api_key_required", "Credimi-Api-Key is required")
	}

	if s.matchesConfiguredUserAPIKey(key) {
		return ctx, nil
	}

	if s.matchesConfiguredInternalAdminAPIKey(key) {
		return ctx, nil
	}

	ok, err := s.validInternalAdminAPIKey(ctx, key)
	if err != nil {
		return ctx, authError(ctx, http.StatusUnauthorized, "invalid_api_key", "Invalid API key provided")
	}
	if !ok {
		return ctx, authError(ctx, http.StatusUnauthorized, "invalid_api_key", "Invalid API key provided")
	}

	return ctx, nil
}

func (s *runnerService) matchesConfiguredUserAPIKey(key string) bool {
	return s.matchesConfiguredAPIKey(key, func(inst utils.Instance) string {
		return inst.UserAPIKey
	})
}

func (s *runnerService) matchesConfiguredInternalAdminAPIKey(key string) bool {
	return s.matchesConfiguredAPIKey(key, func(inst utils.Instance) string {
		return inst.InternalAdminKey
	})
}

func (s *runnerService) matchesConfiguredAPIKey(key string, configuredKey func(utils.Instance) string) bool {
	presented := sha256.Sum256([]byte(key))
	for _, inst := range s.Instances {
		configured := strings.TrimSpace(configuredKey(inst))
		if configured == "" {
			continue
		}
		configuredHash := sha256.Sum256([]byte(configured))
		if subtle.ConstantTimeCompare(presented[:], configuredHash[:]) == 1 {
			return true
		}
	}
	return false
}

func (s *runnerService) validInternalAdminAPIKey(ctx context.Context, key string) (bool, error) {
	for _, inst := range s.Instances {
		instanceURL := strings.TrimSpace(inst.URL)
		if instanceURL == "" {
			continue
		}

		cacheKey := adminAPIKeyCacheKey(instanceURL, key)
		if s.hasCachedAdminAPIKey(cacheKey) {
			return true, nil
		}

		ok, err := s.introspectInternalAdminAPIKey(ctx, instanceURL, key)
		if err != nil {
			continue
		}
		if ok {
			s.cacheAdminAPIKey(cacheKey)
			return true, nil
		}
	}
	return false, nil
}

func (s *runnerService) introspectInternalAdminAPIKey(ctx context.Context, instanceURL, key string) (bool, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		utils.JoinURL(instanceURL, "api", "apikey", "authenticate-internal-admin"),
		nil,
	)
	if err != nil {
		return false, err
	}
	setAPIKeyHeader(req, key)

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	default:
		return false, fmt.Errorf("unexpected admin API key introspection status: %s", resp.Status)
	}
}

func (s *runnerService) hasCachedAdminAPIKey(cacheKey string) bool {
	now := time.Now()
	s.authCacheMu.Lock()
	defer s.authCacheMu.Unlock()

	expiresAt, ok := s.authCache[cacheKey]
	if !ok {
		return false
	}
	if now.After(expiresAt) {
		delete(s.authCache, cacheKey)
		return false
	}
	return true
}

func (s *runnerService) cacheAdminAPIKey(cacheKey string) {
	s.authCacheMu.Lock()
	defer s.authCacheMu.Unlock()
	s.authCache[cacheKey] = time.Now().Add(adminAPIKeyCacheTTL)
}

func adminAPIKeyCacheKey(instanceURL, key string) string {
	sum := sha256.Sum256([]byte(key))
	return strings.TrimRight(instanceURL, "/") + ":" + hex.EncodeToString(sum[:])
}

func authError(ctx context.Context, status int, reason, message string) error {
	name := "unauthorized"
	if status == http.StatusForbidden {
		name = "forbidden"
	}
	domain := "request.validation"
	service, _ := ctx.Value(goa.ServiceKey).(string)
	switch service {
	case credimi.ServiceName:
		return &credimi.APIError{
			Name:    name,
			Code:    status,
			Domain:  domain,
			Reason:  reason,
			Message: message,
		}
	case worker.ServiceName:
		return &worker.APIError{
			Name:    name,
			Code:    status,
			Domain:  domain,
			Reason:  reason,
			Message: message,
		}
	case mobile.ServiceName:
		return &mobile.APIError{
			Name:    name,
			Code:    status,
			Domain:  domain,
			Reason:  reason,
			Message: message,
		}
	}
	return &runner.APIError{
		Name:    name,
		Code:    status,
		Domain:  domain,
		Reason:  reason,
		Message: message,
	}
}
