package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type storePipelineResultPayload struct {
	VideoPath              string   `json:"video_path"`
	LastFramePath          string   `json:"last_frame_path"`
	LogPath                string   `json:"log_path"`
	MaestroScreenshotPaths []string `json:"maestro_screenshot_paths"`
	RunIdentifier          string   `json:"run_identifier"`
	RunnerIdentifier       string   `json:"runner_identifier"`
	Platform               string   `json:"platform"`
}

const maxMaestroScreenshots = 99

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

	screenshotPaths, apiErr := validateMaestroScreenshotPaths(
		payload.MaestroScreenshotPaths,
		s.Deps.ManagedWorkflowRoot,
		s.Deps.FileStore,
	)
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

	for _, screenshotPath := range screenshotPaths {
		if apiErr := addFileToMultipart(writer, "maestro_screenshots", screenshotPath, s.Deps.FileStore); apiErr != nil {
			return nil, apiErr
		}
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

	instance := s.Instance
	storeURL := utils.JoinURL(instance.URL, "api", "wallet", "store-pipeline-result")

	req, err := http.NewRequest("POST", storeURL, &body)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "request failed",
			Message: err.Error(),
		}
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

	artifactPaths := []string{payload.VideoPath, payload.LastFramePath, payload.LogPath}
	artifactPaths = append(artifactPaths, screenshotPaths...)
	for _, resultDir := range managedArtifactDirectories(artifactPaths, s.Deps.ManagedWorkflowRoot) {
		if err := s.Deps.FileStore.RemoveAll(resultDir); err != nil {
			log.Printf("pipeline result cleanup failed for directory %q: %v", filepath.Base(resultDir), err)
		}
	}

	return respBody, nil
}

func validateMaestroScreenshotPaths(paths []string, root string, fileStore FileStore) ([]string, *runner.APIError) {
	if len(paths) > maxMaestroScreenshots {
		return nil, badScreenshotPathError(fmt.Sprintf("maestro_screenshot_paths exceeds the maximum of %d files", maxMaestroScreenshots))
	}

	validated := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, badScreenshotPathError("maestro screenshot path must not be empty")
		}
		cleanPath := filepath.Clean(path)
		if !isPathWithinRoot(cleanPath, root, false) {
			return nil, badScreenshotPathError("maestro screenshot path must be within the managed workflow root")
		}
		if _, ok := seen[cleanPath]; ok {
			continue
		}
		if hasSymlinkComponent(cleanPath, root, fileStore) {
			return nil, badScreenshotPathError("maestro screenshot path must not contain symlinks")
		}

		info, err := fileStore.Lstat(cleanPath)
		if err != nil {
			return nil, badScreenshotPathError("maestro screenshot is missing or unreadable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, badScreenshotPathError("maestro screenshot must not be a symlink")
		}
		if !info.Mode().IsRegular() || !isImageFilename(cleanPath) {
			return nil, badScreenshotPathError("maestro screenshot must be a regular image file")
		}
		file, err := fileStore.Open(cleanPath)
		if err != nil {
			return nil, badScreenshotPathError("maestro screenshot is missing or unreadable")
		}
		if err := file.Close(); err != nil {
			return nil, badScreenshotPathError("maestro screenshot is unreadable")
		}

		seen[cleanPath] = struct{}{}
		validated = append(validated, cleanPath)
	}
	return validated, nil
}

func hasSymlinkComponent(path, root string, fileStore FileStore) bool {
	cleanRoot := filepath.Clean(root)
	relative, err := filepath.Rel(cleanRoot, filepath.Clean(path))
	if err != nil {
		return true
	}
	current := cleanRoot
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := fileStore.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func badScreenshotPathError(message string) *runner.APIError {
	return &runner.APIError{
		Code:    http.StatusBadRequest,
		Domain:  "server",
		Reason:  "invalid maestro screenshots",
		Message: message,
	}
}

func isImageFilename(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func managedArtifactDirectories(paths []string, root string) []string {
	directories := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		directory := filepath.Dir(filepath.Clean(path))
		if !isPathWithinRoot(directory, root, false) {
			continue
		}
		if _, ok := seen[directory]; ok {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	return directories
}

func isPathWithinRoot(path, root string, allowRoot bool) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absRoot, absPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return allowRoot || relative != "."
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
