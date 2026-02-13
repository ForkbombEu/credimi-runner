package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func withPublicOpenAPIServerURL(next http.Handler) http.Handler {
	serverURL, ok := resolvePublicServerURL()
	if !ok {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/docs/openapi3-public.json" {
			next.ServeHTTP(w, r)
			return
		}

		path := filepath.Join(projectRootDirForFS(), "pkg", "gen", "http", "openapi3-public.json")
		content, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "404 page not found", http.StatusNotFound)
			return
		}

		var spec map[string]any
		if err := json.Unmarshal(content, &spec); err != nil {
			http.Error(w, "500 internal error", http.StatusInternalServerError)
			return
		}

		spec["servers"] = []any{
			map[string]any{"url": serverURL},
		}

		body, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			http.Error(w, "500 internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(body, '\n'))
	})
}

func resolvePublicServerURL() (string, bool) {
	domain := strings.TrimSpace(os.Getenv("RUNNER_DOMAIN"))
	if domain == "" || domain == ":80" {
		return "", false
	}

	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		return domain, true
	}

	return "https://" + domain, true
}

