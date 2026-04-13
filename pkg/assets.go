package assets

import (
	"embed"
	"io/fs"
)

//go:embed server/docs/index.html gen/http/openapi.yaml gen/http/openapi3.yaml gen/http/openapi3-public.json
var embeddedFiles embed.FS

func ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(embeddedFiles, name)
}
