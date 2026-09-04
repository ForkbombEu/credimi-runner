package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type storeExecutionScreenshotsPayload struct {
	RunIdentifier    string   `json:"run_identifier"`
	DeviceIdentifier string   `json:"device_identifier"`
	StepID           string   `json:"step_id"`
	ScreenshotPaths  []string `json:"screenshot_paths"`
}

func (s *runnerService) storeExecutionScreenshotsLogic(payload storeExecutionScreenshotsPayload) ([]byte, *runner.APIError) {
	if _, apiErr := s.configuredDevice(payload.DeviceIdentifier); apiErr != nil {
		return nil, apiErr
	}
	if field := missingExecutionScreenshotField(payload); field != "" {
		return nil, &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "missing field",
			Message: field + " is required",
		}
	}

	root := s.Deps.ManagedWorkflowRoot
	if _, configErr := s.currentRuntimeConfig(); configErr == nil {
		var err error
		root, err = deviceArtifactRoot(root, payload.DeviceIdentifier, payload.RunIdentifier)
		if err != nil {
			return nil, badScreenshotPathError(err.Error())
		}
	}
	screenshotPaths, apiErr := validateScreenshotPaths(payload.ScreenshotPaths, root, s.Deps.FileStore)
	if apiErr != nil {
		return nil, apiErr
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"run_identifier":    payload.RunIdentifier,
		"device_identifier": payload.DeviceIdentifier,
		"step_id":           payload.StepID,
	} {
		if err := writer.WriteField(name, value); err != nil {
			return nil, multipartAPIError(err)
		}
	}
	for _, screenshotPath := range screenshotPaths {
		if apiErr := addFileToMultipart(writer, "screenshots", screenshotPath, s.Deps.FileStore); apiErr != nil {
			return nil, apiErr
		}
	}
	if err := writer.Close(); err != nil {
		return nil, multipartAPIError(err)
	}

	instance := s.Instance
	req, err := http.NewRequest(http.MethodPost, utils.JoinURL(instance.URL, "api", "pipeline", "store-step-screenshots"), &body)
	if err != nil {
		return nil, requestAPIError(err)
	}
	apiKey := instance.UserAPIKey
	if apiKey == "" {
		apiKey = instance.InternalAdminKey
	}
	if apiKey == "" {
		return nil, &runner.APIError{
			Code:    http.StatusUnauthorized,
			Domain:  "authorization",
			Reason:  "missing credentials",
			Message: "missing Credimi credentials: set CREDIMI_USER_API_KEY or CREDIMI_INTERNAL_ADMIN_KEY",
		}
	}
	setAPIKeyHeader(req, apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, requestAPIError(err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, requestAPIError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseUpstreamRunnerAPIError(resp.StatusCode, respBody)
	}
	if apiErr := validateExecutionScreenshotResponse(respBody); apiErr != nil {
		return nil, apiErr
	}

	s.cleanupExecutionScreenshots(screenshotPaths)
	return respBody, nil
}

func missingExecutionScreenshotField(payload storeExecutionScreenshotsPayload) string {
	switch {
	case strings.TrimSpace(payload.RunIdentifier) == "":
		return "run_identifier"
	case strings.TrimSpace(payload.DeviceIdentifier) == "":
		return "device_identifier"
	case strings.TrimSpace(payload.StepID) == "":
		return "step_id"
	case len(payload.ScreenshotPaths) == 0:
		return "screenshot_paths"
	default:
		return ""
	}
}

func validateExecutionScreenshotResponse(body []byte) *runner.APIError {
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil || response == nil {
		return &runner.APIError{
			Code:    http.StatusBadGateway,
			Domain:  "upstream",
			Reason:  "invalid response",
			Message: "store step screenshots returned a non-object JSON body",
		}
	}
	urls, ok := response["screenshot_urls"].([]any)
	if !ok || len(urls) == 0 {
		return &runner.APIError{
			Code:    http.StatusBadGateway,
			Domain:  "upstream",
			Reason:  "invalid response",
			Message: "store step screenshots returned no screenshot_urls",
		}
	}
	for _, value := range urls {
		url, ok := value.(string)
		if !ok || strings.TrimSpace(url) == "" {
			return &runner.APIError{
				Code:    http.StatusBadGateway,
				Domain:  "upstream",
				Reason:  "invalid response",
				Message: "store step screenshots returned an invalid screenshot URL",
			}
		}
	}
	return nil
}

func (s *runnerService) cleanupExecutionScreenshots(paths []string) {
	directories := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if err := s.Deps.FileStore.Remove(path); err != nil {
			log.Printf("step screenshot cleanup failed for file %q: %v", filepath.Base(path), err)
			continue
		}
		directories[filepath.Dir(path)] = struct{}{}
	}
	for directory := range directories {
		if !isPathWithinRoot(directory, s.Deps.ManagedWorkflowRoot, false) {
			continue
		}
		entries, err := s.Deps.FileStore.ReadDir(directory)
		if err != nil {
			log.Printf("step screenshot cleanup could not inspect directory %q: %v", filepath.Base(directory), err)
			continue
		}
		if len(entries) != 0 {
			continue
		}
		if err := s.Deps.FileStore.Remove(directory); err != nil {
			log.Printf("step screenshot cleanup failed for directory %q: %v", filepath.Base(directory), err)
		}
	}
}

func multipartAPIError(err error) *runner.APIError {
	return &runner.APIError{Code: http.StatusInternalServerError, Domain: "server", Reason: "multipart failed", Message: err.Error()}
}

func requestAPIError(err error) *runner.APIError {
	return &runner.APIError{Code: http.StatusInternalServerError, Domain: "server", Reason: "request failed", Message: err.Error()}
}
