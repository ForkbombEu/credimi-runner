package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type storePipelineResultPayload struct {
	InstanceURL      string `json:"instance_url"`
	VideoPath        string `json:"video_path"`
	LastFramePath    string `json:"last_frame_path"`
	LogPath          string `json:"log_path"`
	RunIdentifier    string `json:"run_identifier"`
	RunnerIdentifier string `json:"runner_identifier"`
	Platform         string `json:"platform"`
}

func (s *runnerService) storePipelineResultLogic(payload storePipelineResultPayload) ([]byte, *runner.APIError) {
	if payload.VideoPath == "" {
		return nil, &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "missing field",
			Message: "result_path is required",
		}
	}

	platform, apiErr := normalizeInstallerPlatform(payload.Platform)
	if apiErr != nil {
		return nil, apiErr
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if apiErr := addFileToMultipart(writer, "result_video", payload.VideoPath, s.Deps.FileStore); apiErr != nil {
		return nil, apiErr
	}

	if apiErr := addFileToMultipart(writer, "last_frame", payload.LastFramePath, s.Deps.FileStore); apiErr != nil {
		return nil, apiErr
	}

	if apiErr := addFileToMultipart(writer, "logfile", payload.LogPath, s.Deps.FileStore); apiErr != nil {
		return nil, apiErr
	}

	if err := writer.WriteField("runner_identifier", payload.RunnerIdentifier); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	if err := writer.WriteField("run_identifier", payload.RunIdentifier); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	if err := writer.WriteField("platform", platform); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	if err := writer.Close(); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	storeURL := utils.JoinURL(payload.InstanceURL, "api", "wallet", "store-pipeline-result")

	req, err := http.NewRequest("POST", storeURL, &body)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "request failed",
			Message: err.Error(),
		}
	}

	instance, err := s.getInstanceByURL(payload.InstanceURL)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "invalid instance url",
			Message: err.Error(),
		}
	}

	token, err := s.Deps.TokenProvider(instance)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusUnauthorized,
			Domain:  "authorization",
			Reason:  "invalid token",
			Message: "failed to get admin token: " + err.Error(),
		}
	}

	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "request failed",
			Message: err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "request failed",
			Message: err.Error(),
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseUpstreamRunnerAPIError(resp.StatusCode, respBody)
	}

	resultDir := filepath.Dir(payload.VideoPath)
	if err := s.Deps.FileStore.RemoveAll(resultDir); err != nil {
		log.Printf("cleanup failed for %s: %v", resultDir, err)
	}

	return respBody, nil
}

func addFileToMultipart(writer *multipart.Writer, fieldName, filePath string, fileStore FileStore) *runner.APIError {
	file, err := fileStore.Open(filePath)
	if err != nil {
		return &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "file open failed",
			Message: err.Error(),
		}
	}
	defer file.Close()

	part, err := writer.CreateFormFile(fieldName, filepath.Base(filePath))
	if err != nil {
		return &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	if _, err := io.Copy(part, file); err != nil {
		return &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "multipart failed",
			Message: err.Error(),
		}
	}

	return nil
}

func parseUpstreamRunnerAPIError(statusCode int, body []byte) *runner.APIError {
	decoder := json.NewDecoder(bytes.NewReader(body))

	var direct runner.APIError
	if err := decoder.Decode(&direct); err == nil {
		if direct.Code == 0 {
			direct.Code = statusCode
		}
		return &direct
	}

	decoder = json.NewDecoder(bytes.NewReader(body))
	var wrapped struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Errors  []struct {
				Domain  string `json:"domain"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := decoder.Decode(&wrapped); err == nil {
		apiErr := &runner.APIError{
			Code:    statusCode,
			Domain:  "upstream",
			Reason:  "request failed",
			Message: wrapped.Error.Message,
		}
		if wrapped.Error.Code != 0 {
			apiErr.Code = wrapped.Error.Code
		}
		if len(wrapped.Error.Errors) > 0 {
			first := wrapped.Error.Errors[0]
			if first.Domain != "" {
				apiErr.Domain = first.Domain
			}
			if first.Reason != "" {
				apiErr.Reason = first.Reason
			}
			if first.Message != "" {
				apiErr.Message = first.Message
			}
		}
		if apiErr.Message == "" {
			apiErr.Message = fmt.Sprintf("upstream request failed with status %d", statusCode)
		}
		return apiErr
	}

	return &runner.APIError{
		Code:    statusCode,
		Domain:  "upstream",
		Reason:  "request failed",
		Message: fmt.Sprintf("upstream request failed with status %d: %s", statusCode, bytes.TrimSpace(body)),
	}
}
