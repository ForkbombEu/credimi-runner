package server

import (
	"bytes"
	"context"
	"io"

	runnerassets "github.com/forkbombeu/credimi-runner/pkg"
	docs "github.com/forkbombeu/credimi-runner/pkg/gen/docs"
)

func docsAssetBody(content []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(content))
}

func readDocsIndexHTML() ([]byte, error) {
	return runnerassets.ReadFile("server/docs/index.html")
}

func readOpenAPIYAML() ([]byte, error) {
	return runnerassets.ReadFile("gen/http/openapi.yaml")
}

func readOpenAPI3YAML() ([]byte, error) {
	return runnerassets.ReadFile("gen/http/openapi3.yaml")
}

func readOpenAPI3PublicJSON() ([]byte, error) {
	return runnerassets.ReadFile("gen/http/openapi3-public.json")
}

func (s *runnerService) Docs(ctx context.Context) (string, error) {
	content, err := readDocsIndexHTML()
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *runnerService) DocsOpenapi(ctx context.Context) (string, error) {
	content, err := readOpenAPIYAML()
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *runnerService) Index(ctx context.Context) (res *docs.IndexResult, body io.ReadCloser, err error) {
	content, err := readDocsIndexHTML()
	if err != nil {
		return nil, nil, wrapDocsAPIError(internalDocsAssetError("read docs index", err))
	}

	res = &docs.IndexResult{
		Length:   int64(len(content)),
		Encoding: "text/html; charset=utf-8",
	}
	body = docsAssetBody(content)
	return res, body, nil
}

func (s *runnerService) Openapi(ctx context.Context) (res *docs.OpenapiResult, body io.ReadCloser, err error) {
	content, err := readOpenAPIYAML()
	if err != nil {
		return nil, nil, wrapDocsAPIError(internalDocsAssetError("read openapi yaml", err))
	}

	res = &docs.OpenapiResult{
		Length:   int64(len(content)),
		Encoding: "application/yaml; charset=utf-8",
	}
	body = docsAssetBody(content)
	return res, body, nil
}

func (s *runnerService) Openapi3(ctx context.Context) (res *docs.Openapi3Result, body io.ReadCloser, err error) {
	content, err := readOpenAPI3YAML()
	if err != nil {
		return nil, nil, wrapDocsAPIError(internalDocsAssetError("read openapi3 yaml", err))
	}

	res = &docs.Openapi3Result{
		Length:   int64(len(content)),
		Encoding: "application/yaml; charset=utf-8",
	}
	body = docsAssetBody(content)
	return res, body, nil
}

func (s *runnerService) Openapi3Public(ctx context.Context) (res *docs.Openapi3PublicResult, body io.ReadCloser, err error) {
	content, err := readOpenAPI3PublicContent()
	if err != nil {
		return nil, nil, err
	}

	res = &docs.Openapi3PublicResult{
		Length:   int64(len(content)),
		Encoding: "application/json; charset=utf-8",
	}
	body = docsAssetBody(content)
	return res, body, nil
}
