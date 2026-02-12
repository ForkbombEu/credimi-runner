package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	inputPath := filepath.Join("..", "gen", "http", "openapi3.json")
	outputPath := filepath.Join("..", "gen", "http", "openapi3-public.json")

	if err := generatePublicOpenAPI(inputPath, outputPath); err != nil {
		fail("generate openapi public", err)
	}
}

func generatePublicOpenAPI(inputPath, outputPath string) error {
	input, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	var spec map[string]any
	if err := json.Unmarshal(input, &spec); err != nil {
		return fmt.Errorf("decode input JSON: %w", err)
	}
	if err := filterPublicSpec(spec, inputPath); err != nil {
		return err
	}
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode output JSON: %w", err)
	}

	out = append(out, '\n')
	if err := os.WriteFile(outputPath, out, 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func filterPublicSpec(spec map[string]any, inputPath string) error {
	pathsRaw, ok := spec["paths"]
	if !ok {
		return fmt.Errorf("paths not found in %s", inputPath)
	}

	paths, ok := pathsRaw.(map[string]any)
	if !ok {
		return fmt.Errorf("expected object, got %T", pathsRaw)
	}

	for p := range paths {
		if strings.HasPrefix(p, "/docs") {
			delete(paths, p)
		}
	}
	for p, pathItemRaw := range paths {
		pathItem, ok := pathItemRaw.(map[string]any)
		if !ok {
			continue
		}
		for key, operationRaw := range pathItem {
			if !isHTTPMethod(key) {
				continue
			}
			operation, ok := operationRaw.(map[string]any)
			if !ok {
				continue
			}
			if operationHasTag(operation, "docs") {
				delete(pathItem, key)
			}
		}
		if !hasHTTPMethods(pathItem) {
			delete(paths, p)
		}
	}

	if tagsRaw, ok := spec["tags"]; ok {
		if tags, ok := tagsRaw.([]any); ok {
			filtered := make([]any, 0, len(tags))
			for _, tag := range tags {
				tagObj, ok := tag.(map[string]any)
				if !ok {
					filtered = append(filtered, tag)
					continue
				}
				name, _ := tagObj["name"].(string)
				if name == "docs" {
					continue
				}
				filtered = append(filtered, tagObj)
			}
			spec["tags"] = filtered
		}
	}
	return nil
}

func operationHasTag(operation map[string]any, tag string) bool {
	tagsRaw, ok := operation["tags"]
	if !ok {
		return false
	}
	tags, ok := tagsRaw.([]any)
	if !ok {
		return false
	}
	for _, t := range tags {
		name, ok := t.(string)
		if !ok {
			continue
		}
		if name == tag {
			return true
		}
	}
	return false
}

func hasHTTPMethods(pathItem map[string]any) bool {
	for key := range pathItem {
		if isHTTPMethod(key) {
			return true
		}
	}
	return false
}

func isHTTPMethod(method string) bool {
	switch strings.ToLower(method) {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	default:
		return false
	}
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "generate_openapi_public: %s: %v\n", step, err)
	os.Exit(1)
}
