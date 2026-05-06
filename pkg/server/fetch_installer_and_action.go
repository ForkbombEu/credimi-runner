package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type fetchInstallerAndActionPayload struct {
	InstanceURL       string `json:"instance_url"`
	VersionIdentifier string `json:"version_identifier"`
	ActionIdentifier  string `json:"action_identifier"`
	Platform          string `json:"platform"`
	SkipInstaller     bool   `json:"skip_installer,omitempty"`
}

type fetchInstallerAndActionResult struct {
	InstallerPath string
	VersionID     string
	Code          *string
}

func (s *runnerService) fetchInstallerAndActionLogic(payload fetchInstallerAndActionPayload) (*fetchInstallerAndActionResult, *runner.APIError) {
	platform, apiErr := normalizeInstallerPlatform(payload.Platform)
	if apiErr != nil {
		return nil, apiErr
	}

	instance, err := s.getInstanceByURL(payload.InstanceURL)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusBadRequest,
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
			Message: "failed to get auth token: " + err.Error(),
		}
	}

	validateURL := utils.JoinURL(payload.InstanceURL, "api", "canonify", "identifier", "validate")
	getInstallerURL := utils.JoinURL(payload.InstanceURL, "api", "wallet", "get-installer-md5-or-etag")

	var actionCode *string
	if payload.ActionIdentifier != "" {
		code, err := validateActionIdentifier(validateURL, payload.ActionIdentifier, token, instance.InternalAdminKey, s.Deps.HTTPClient)
		if err != nil {
			var apiErr *runner.APIError
			if errors.As(err, &apiErr) {
				return nil, apiErr
			}
			return nil, &runner.APIError{
				Code:    http.StatusBadGateway,
				Domain:  "CredimiAPI",
				Reason:  "Not valid action identifier",
				Message: err.Error(),
			}
		}
		actionCode = &code
	}
	if payload.SkipInstaller {
		return &fetchInstallerAndActionResult{
			InstallerPath: "",
			VersionID:     payload.VersionIdentifier,
			Code:          actionCode,
		}, nil
	}

	md5ReqBodyMap := map[string]string{
		"wallet_version_identifier": payload.VersionIdentifier,
		"platform":                  platform,
	}

	if walletID, ok := deriveWalletIdentifier(payload.VersionIdentifier, payload.ActionIdentifier); ok {
		md5ReqBodyMap["wallet_identifier"] = walletID
	}

	md5ReqBody, err := json.Marshal(md5ReqBodyMap)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "marshal failed",
			Message: "failed to marshal request body: " + err.Error(),
		}
	}

	req, _ := http.NewRequest("POST", getInstallerURL, bytes.NewReader(md5ReqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req, instance.InternalAdminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusBadGateway,
			Domain:  "CredimiAPI",
			Reason:  "get-installer failed",
			Message: "failed to call get-installer endpoint: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "read failed",
			Message: "failed to read get-installer response: " + err.Error(),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseUpstreamRunnerAPIError(resp.StatusCode, respBody)
	}

	var md5Resp struct {
		RecordID            string `json:"record_id"`
		InstallerName       string `json:"installer_name"`
		InstallerIdentifier string `json:"installer_identifier"`
		VersionID           string `json:"version_id"`
	}
	if err := json.Unmarshal(respBody, &md5Resp); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "parse failed",
			Message: "failed to parse get-installer response: " + err.Error(),
		}
	}

	if md5Resp.InstallerName == "" || md5Resp.InstallerIdentifier == "" {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "missing fields",
			Message: fmt.Sprintf("missing fields in get-installer response: %s", string(respBody)),
		}
	}

	fileURL := utils.JoinURL(payload.InstanceURL, "api", "files", "wallet_versions", md5Resp.RecordID, md5Resp.InstallerName)
	path, err := downloadInstallerIfMissing(
		fileURL,
		token,
		md5Resp.InstallerIdentifier,
		md5Resp.InstallerName,
		platform,
		instance.InternalAdminKey,
		s.Deps.HTTPClient,
		s.Deps.FileStore,
	)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "credimiAPI",
			Reason:  "download failed",
			Message: "failed to download file: " + err.Error(),
		}
	}

	return &fetchInstallerAndActionResult{
		InstallerPath: path,
		VersionID:     md5Resp.VersionID,
		Code:          actionCode,
	}, nil
}

func downloadInstallerIfMissing(fileURL, token, localName, installerName, platform, internalAdminKey string, client HTTPClient, fileStore FileStore) (string, error) {
	localPath, err := downloadFileIfMissing(fileURL, token, localName, installerName, internalAdminKey, client, fileStore)
	if err != nil {
		return "", err
	}

	if platform != "ios" || !strings.EqualFold(filepath.Ext(installerName), ".zip") {
		return localPath, nil
	}

	return unzipIOSAppIfNeeded(localPath, localName, fileStore)
}

func downloadFileIfMissing(fileURL, token, localName, installerName, internalAdminKey string, client HTTPClient, fileStore FileStore) (string, error) {
	if err := fileStore.MkdirAll("apps", 0755); err != nil {
		return "", fmt.Errorf("failed to create apps directory: %v", err)
	}

	localPath := filepath.Join("apps", localName+filepath.Ext(installerName))

	if _, err := fileStore.Stat(localPath); err == nil {
		return localPath, nil
	}

	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req, internalAdminKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := fileStore.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %v", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed to save file: %v", err)
	}

	return localPath, nil
}

func unzipIOSAppIfNeeded(archivePath, localName string, fileStore FileStore) (string, error) {
	markerPath := filepath.Join("apps", localName+".app-path")
	if appPath, err := readStoredAppPath(markerPath, fileStore); err == nil {
		if _, err := fileStore.Stat(appPath); err == nil {
			return appPath, nil
		}
	}

	archiveData, err := readStoredFile(archivePath, fileStore)
	if err != nil {
		return "", fmt.Errorf("failed to read ios archive: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return "", fmt.Errorf("failed to open ios archive: %v", err)
	}

	extractRoot := filepath.Join("apps", localName)
	if err := fileStore.RemoveAll(extractRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to clear extracted ios app: %v", err)
	}
	if err := fileStore.MkdirAll(extractRoot, 0755); err != nil {
		return "", fmt.Errorf("failed to create ios extraction directory: %v", err)
	}

	appPath, err := extractIOSAppArchive(reader, extractRoot, fileStore)
	if err != nil {
		return "", err
	}

	if err := writeStoredAppPath(markerPath, appPath, fileStore); err != nil {
		return "", fmt.Errorf("failed to persist ios app path: %v", err)
	}

	return appPath, nil
}

func extractIOSAppArchive(reader *zip.Reader, extractRoot string, fileStore FileStore) (string, error) {
	var appPath string

	for _, file := range reader.File {
		cleanName := filepath.Clean(file.Name)
		if cleanName == "." {
			continue
		}
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return "", fmt.Errorf("invalid zip entry path: %s", file.Name)
		}

		targetPath := filepath.Join(extractRoot, cleanName)
		if targetPath != extractRoot && !strings.HasPrefix(targetPath, extractRoot+string(os.PathSeparator)) {
			return "", fmt.Errorf("invalid zip entry path: %s", file.Name)
		}

		if candidate := iosAppBundlePath(extractRoot, cleanName); candidate != "" && appPath == "" {
			appPath = candidate
		}

		if file.FileInfo().IsDir() {
			if err := fileStore.MkdirAll(targetPath, file.Mode()); err != nil {
				return "", fmt.Errorf("failed to create extracted directory %s: %v", file.Name, err)
			}
			continue
		}

		if err := fileStore.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return "", fmt.Errorf("failed to create extracted directory for %s: %v", file.Name, err)
		}

		entryReader, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("failed to open zip entry %s: %v", file.Name, err)
		}

		out, err := fileStore.Create(targetPath)
		if err != nil {
			entryReader.Close()
			return "", fmt.Errorf("failed to create extracted file %s: %v", file.Name, err)
		}

		if _, err := io.Copy(out, entryReader); err != nil {
			out.Close()
			entryReader.Close()
			return "", fmt.Errorf("failed to extract zip entry %s: %v", file.Name, err)
		}
		if err := out.Close(); err != nil {
			entryReader.Close()
			return "", fmt.Errorf("failed to close extracted file %s: %v", file.Name, err)
		}
		if err := entryReader.Close(); err != nil {
			return "", fmt.Errorf("failed to close zip entry %s: %v", file.Name, err)
		}
	}

	if appPath == "" {
		return "", errors.New("ios zip archive does not contain an .app bundle")
	}

	return appPath, nil
}

func iosAppBundlePath(extractRoot, entryName string) string {
	parts := strings.Split(filepath.Clean(entryName), string(os.PathSeparator))
	for i, part := range parts {
		if strings.HasSuffix(strings.ToLower(part), ".app") {
			return filepath.Join(append([]string{extractRoot}, parts[:i+1]...)...)
		}
	}
	return ""
}

func readStoredFile(path string, fileStore FileStore) ([]byte, error) {
	file, err := fileStore.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}

func readStoredAppPath(path string, fileStore FileStore) (string, error) {
	data, err := readStoredFile(path, fileStore)
	if err != nil {
		return "", err
	}

	appPath := strings.TrimSpace(string(data))
	if appPath == "" {
		return "", os.ErrNotExist
	}

	return appPath, nil
}

func writeStoredAppPath(path, appPath string, fileStore FileStore) error {
	file, err := fileStore.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.WriteString(file, appPath)
	return err
}

func validateActionIdentifier(url, identifier, token, internalAdminKey string, client HTTPClient) (string, error) {
	body, _ := json.Marshal(map[string]string{"canonified_name": identifier})
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req, internalAdminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call validate endpoint: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", parseUpstreamRunnerAPIError(resp.StatusCode, respBody)
	}

	var data struct {
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return "", fmt.Errorf("failed to parse validate response: %v", err)
	}

	code, ok := data.Record["code"].(string)
	if !ok || code == "" {
		return "", fmt.Errorf("record missing 'code' field")
	}

	return code, nil
}

func deriveWalletIdentifier(versionID, actionID string) (string, bool) {
	if versionID != "" || actionID == "" {
		return "", false
	}
	parts := strings.Split(actionID, "/")
	if len(parts) < 2 {
		return "", false
	}
	return strings.Join(parts[:len(parts)-1], "/"), true
}

func normalizeInstallerPlatform(platform string) (string, *runner.APIError) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "android", "ios":
		return strings.ToLower(strings.TrimSpace(platform)), nil
	default:
		return "", &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid platform",
			Message: "supported values are android or ios",
		}
	}
}
