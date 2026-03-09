package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
)

type fetchApkAndActionPayload struct {
	InstanceURL       string `json:"instance_url"`
	VersionIdentifier string `json:"version_identifier"`
	ActionIdentifier  string `json:"action_identifier"`
}

type fetchApkAndActionResult struct {
	ApkPath   string
	VersionID string
	Code      *string
}

func (s *runnerService) fetchApkAndActionLogic(payload fetchApkAndActionPayload) (*fetchApkAndActionResult, *runner.APIError) {
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
			Message: "failed to get admin token: " + err.Error(),
		}
	}

	validateURL := utils.JoinURL(payload.InstanceURL, "api", "canonify", "identifier", "validate")
	getMD5URL := utils.JoinURL(payload.InstanceURL, "api", "wallet", "get-apk-md5-or-etag")

	var actionCode *string
	if payload.ActionIdentifier != "" {
		code, err := validateActionIdentifier(validateURL, payload.ActionIdentifier, token, s.Deps.HTTPClient)
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

	md5ReqBodyMap := map[string]string{
		"wallet_version_identifier": payload.VersionIdentifier,
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

	req, _ := http.NewRequest("POST", getMD5URL, bytes.NewReader(md5ReqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusBadGateway,
			Domain:  "CredimiAPI",
			Reason:  "get-md5 failed",
			Message: "failed to call get-md5 endpoint: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "read failed",
			Message: "failed to read get-md5 response: " + err.Error(),
		}
	}
	if resp.StatusCode != http.StatusOK {
		var errResp runner.APIError
		if err := json.Unmarshal(respBody, &errResp); err != nil {
			return nil, &runner.APIError{
				Code:    http.StatusInternalServerError,
				Domain:  "server",
				Reason:  "unmarshal failed",
				Message: "failed to unmarshal get-md5 response: " + err.Error(),
			}
		}
		return nil, &errResp
	}

	var md5Resp struct {
		RecordID      string `json:"record_id"`
		ApkName       string `json:"apk_name"`
		ApkIdentifier string `json:"apk_identifier"`
		VersionID     string `json:"version_id"`
	}
	if err := json.Unmarshal(respBody, &md5Resp); err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "parse failed",
			Message: "failed to parse get-md5 response: " + err.Error(),
		}
	}

	if md5Resp.ApkName == "" || md5Resp.ApkIdentifier == "" {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "missing fields",
			Message: fmt.Sprintf("missing fields in get-md5 response: %s", string(respBody)),
		}
	}

	fileURL := utils.JoinURL(payload.InstanceURL, "api", "files", "wallet_versions", md5Resp.RecordID, md5Resp.ApkName)
	path, err := downloadFileIfMissing(fileURL, token, md5Resp.ApkIdentifier, s.Deps.HTTPClient, s.Deps.FileStore)
	if err != nil {
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "credimiAPI",
			Reason:  "download failed",
			Message: "failed to download file: " + err.Error(),
		}
	}

	return &fetchApkAndActionResult{
		ApkPath:   path,
		VersionID: md5Resp.VersionID,
		Code:      actionCode,
	}, nil
}

func downloadFileIfMissing(fileURL, token, localName string, client HTTPClient, fileStore FileStore) (string, error) {
	if err := fileStore.MkdirAll("apps", 0755); err != nil {
		return "", fmt.Errorf("failed to create apps directory: %v", err)
	}

	localPath := filepath.Join("apps", localName+".apk")

	if _, err := fileStore.Stat(localPath); err == nil {
		return localPath, nil
	}

	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req)
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

func validateActionIdentifier(url, identifier, token string, client HTTPClient) (string, error) {
	body, _ := json.Marshal(map[string]string{"canonified_name": identifier})
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	setInternalAdminKeyHeader(req)
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
		var errResp runner.APIError
		if err := json.Unmarshal(respBody, &errResp); err != nil {
			return "", fmt.Errorf("validate failed: %s", resp.Status)
		}
		return "", &errResp
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
