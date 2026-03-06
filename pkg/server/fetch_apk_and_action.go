package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/forkbombeu/credimi-runner/pkg/gen/runner"
	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"goa.design/clue/log"
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

func (s *runnerService) fetchApkAndActionLogic(ctx context.Context, payload fetchApkAndActionPayload) (*fetchApkAndActionResult, *runner.APIError) {
	ctx = log.With(ctx,
		log.KV{K: "version.identifier", V: payload.VersionIdentifier},
		log.KV{K: "instance.url", V: payload.InstanceURL},
		log.KV{K: "action.identifier", V: payload.ActionIdentifier},
	)
	log.Info(ctx, log.KV{K: "msg", V: "Starting fetchApkAndActionLogic"})

	instance, err := s.getInstanceByURL(payload.InstanceURL)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "invalid instance url"})
		return nil, &runner.APIError{
			Code:    http.StatusBadRequest,
			Domain:  "server",
			Reason:  "invalid instance url",
			Message: err.Error(),
		}
	}

	token, err := s.Deps.TokenProvider(instance)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "token acquisition failed"})
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
		log.Info(ctx, log.KV{K: "msg", V: "validating action identifier"})

		code, err := validateActionIdentifier(ctx, validateURL, payload.ActionIdentifier, token, s.Deps.HTTPClient)
		if err != nil {
			log.Error(ctx, err, log.KV{K: "msg", V: "action validation failed"})
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
		log.Info(ctx, log.KV{K: "msg", V: "action validated"}, log.KV{K: "action_code", V: code})
		actionCode = &code
	}

	log.Info(ctx, log.KV{K: "msg", V: "getting APK MD5"})

	md5ReqBodyMap := map[string]string{
		"wallet_version_identifier": payload.VersionIdentifier,
	}

	if walletID, ok := deriveWalletIdentifier(payload.VersionIdentifier, payload.ActionIdentifier); ok {
		md5ReqBodyMap["wallet_identifier"] = walletID
		log.Info(ctx, log.KV{K: "msg", V: "derived wallet identifier"}, log.KV{K: "wallet_id", V: walletID})
	}

	md5ReqBody, err := json.Marshal(md5ReqBodyMap)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to marshal MD5 request"})
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "marshal failed",
			Message: "failed to marshal request body: " + err.Error(),
		}
	}

	req, _ := http.NewRequest("POST", getMD5URL, bytes.NewReader(md5ReqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Deps.HTTPClient.Do(req)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "MD5 request failed"})
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
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to read MD5 response"})
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "read failed",
			Message: "failed to read get-md5 response: " + err.Error(),
		}
	}
	log.Info(ctx,
		log.KV{K: "msg", V: "MD5 response received"},
		log.KV{K: "status_code", V: resp.StatusCode},
		log.KV{K: "response_size", V: len(respBody)},
	)
	if resp.StatusCode != http.StatusOK {
		var errResp runner.APIError
		if err := json.Unmarshal(respBody, &errResp); err != nil {
			log.Error(ctx, err, log.KV{K: "msg", V: "failed to parse error response"})
			return nil, &runner.APIError{
				Code:    http.StatusInternalServerError,
				Domain:  "server",
				Reason:  "unmarshal failed",
				Message: "failed to unmarshal get-md5 response: " + err.Error(),
			}
		}
		log.Error(ctx, errors.New(errResp.Message),
			log.KV{K: "msg", V: "MD5 request returned error"},
			log.KV{K: "error_reason", V: errResp.Reason},
		)
		return nil, &errResp
	}

	var md5Resp struct {
		RecordID      string `json:"record_id"`
		ApkName       string `json:"apk_name"`
		ApkIdentifier string `json:"apk_identifier"`
		VersionID     string `json:"version_id"`
	}
	if err := json.Unmarshal(respBody, &md5Resp); err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to parse MD5 response"})
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "parse failed",
			Message: "failed to parse get-md5 response: " + err.Error(),
		}
	}

	log.Info(ctx,
		log.KV{K: "msg", V: "MD5 response parsed"},
		log.KV{K: "record_id", V: md5Resp.RecordID},
		log.KV{K: "apk_name", V: md5Resp.ApkName},
		log.KV{K: "apk_identifier", V: md5Resp.ApkIdentifier},
		log.KV{K: "version_id", V: md5Resp.VersionID},
	)

	if md5Resp.ApkName == "" || md5Resp.ApkIdentifier == "" {
		err := fmt.Errorf("missing fields in response: %s", string(respBody))
		log.Error(ctx, err, log.KV{K: "msg", V: "invalid MD5 response"})
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "server",
			Reason:  "missing fields",
			Message: fmt.Sprintf("missing fields in get-md5 response: %s", string(respBody)),
		}
	}

	log.Info(ctx, log.KV{K: "msg", V: "downloading APK"})

	fileURL := utils.JoinURL(payload.InstanceURL, "api", "files", "wallet_versions", md5Resp.RecordID, md5Resp.ApkName)

	path, err := downloadFileIfMissing(ctx, fileURL, token, md5Resp.ApkIdentifier, s.Deps.HTTPClient, s.Deps.FileStore)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "download failed"})
		return nil, &runner.APIError{
			Code:    http.StatusInternalServerError,
			Domain:  "credimiAPI",
			Reason:  "download failed",
			Message: "failed to download file: " + err.Error(),
		}
	}

	log.Info(ctx,
		log.KV{K: "msg", V: "APK download complete"},
		log.KV{K: "apk_path", V: path},
	)

	log.Info(ctx, log.KV{K: "msg", V: "fetchApkAndAction completed successfully"})
	return &fetchApkAndActionResult{
		ApkPath:   path,
		VersionID: md5Resp.VersionID,
		Code:      actionCode,
	}, nil
}

func downloadFileIfMissing(ctx context.Context, fileURL, token, localName string, client HTTPClient, fileStore FileStore) (string, error) {
	ctx = log.With(ctx, log.KV{K: "file_url", V: fileURL}, log.KV{K: "local_name", V: localName})
	if err := fileStore.MkdirAll("apps", 0755); err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to create apps directory"})
		return "", fmt.Errorf("failed to create apps directory: %v", err)
	}

	localPath := filepath.Join("apps", localName+".apk")
	log.Info(ctx, log.KV{K: "msg", V: "checking if file exists"}, log.KV{K: "local_path", V: localPath})

	if _, err := fileStore.Stat(localPath); err == nil {
		log.Info(ctx, log.KV{K: "msg", V: "file already exists, skipping download"})
		return localPath, nil
	}
	log.Info(ctx, log.KV{K: "msg", V: "downloading file"})

	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to create request"})
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "download request failed"})
		return "", fmt.Errorf("failed to download file: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Error(ctx, err, log.KV{K: "status_code", V: resp.StatusCode})
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := fileStore.Create(localPath)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to create local file"})
		return "", fmt.Errorf("failed to create file: %v", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to save file"})
		return "", fmt.Errorf("failed to save file: %v", err)
	}
	log.Info(ctx,
		log.KV{K: "msg", V: "file downloaded successfully"},
	)

	return localPath, nil
}

func validateActionIdentifier(ctx context.Context, url, identifier, token string, client HTTPClient) (string, error) {
	ctx = log.With(ctx, log.KV{K: "validate_url", V: url}, log.KV{K: "identifier", V: identifier})
	log.Info(ctx, log.KV{K: "msg", V: "validating action identifier"})

	body, _ := json.Marshal(map[string]string{"canonified_name": identifier})
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "validate request failed"})
		return "", fmt.Errorf("failed to call validate endpoint: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to read validate response"})
		return "", fmt.Errorf("failed to read response body: %v", err)
	}
	log.Info(ctx,
		log.KV{K: "msg", V: "validate response received"},
		log.KV{K: "status_code", V: resp.StatusCode},
	)

	if resp.StatusCode != http.StatusOK {
		var errResp runner.APIError
		if err := json.Unmarshal(respBody, &errResp); err != nil {
			log.Error(ctx, err, log.KV{K: "msg", V: "validation failed with no parseable error"})
			return "", fmt.Errorf("validate failed: %s", resp.Status)
		}
		log.Error(ctx, errors.New(errResp.Message),
			log.KV{K: "msg", V: "validation failed"},
			log.KV{K: "error_reason", V: errResp.Reason},
		)
		return "", &errResp
	}

	var data struct {
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		log.Error(ctx, err, log.KV{K: "msg", V: "failed to parse validate response"})
		return "", fmt.Errorf("failed to parse validate response: %v", err)
	}

	code, ok := data.Record["code"].(string)
	if !ok || code == "" {
		err := fmt.Errorf("record missing 'code' field")
		log.Error(ctx, err, log.KV{K: "msg", V: "invalid validate response format"})
		return "", err
	}
	log.Info(ctx, log.KV{K: "msg", V: "action validated successfully"}, log.KV{K: "code", V: code})
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
