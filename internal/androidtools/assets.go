package androidtools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultBaseAVDArchiveURL = "https://files.pn-a.com/credimi_base_image.tar.gz"
	DefaultGoldenArchiveURL  = "https://files.pn-a.com/credimi_golden.tar.gz"
)

type DownloadProgress struct {
	Phase string `json:"phase"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

var androidAssetHTTPClient = http.DefaultClient

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func AVDAssetsExist(avdHome, avdName string) bool {
	if strings.TrimSpace(avdHome) == "" || strings.TrimSpace(avdName) == "" {
		return false
	}
	return pathExists(filepath.Join(avdHome, avdName+".avd")) && pathExists(filepath.Join(avdHome, avdName+".ini"))
}

func GoldenAssetsExist(goldenRoot, goldenLeaf string) bool {
	goldenRoot = strings.TrimSpace(goldenRoot)
	goldenLeaf = strings.Trim(strings.TrimSpace(goldenLeaf), `/\\`)
	return goldenRoot != "" && goldenLeaf != "" && pathExists(filepath.Join(goldenRoot, goldenLeaf))
}

func ListAVDOptions(avdHome string) []string {
	entries, err := os.ReadDir(avdHome)
	if err != nil {
		return nil
	}
	options := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".avd") {
			name := strings.TrimSuffix(entry.Name(), ".avd")
			if AVDAssetsExist(avdHome, name) {
				options = append(options, name)
			}
		}
	}
	sort.Strings(options)
	return options
}

func ListGoldenOptions(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	options := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			options = append(options, entry.Name())
		}
	}
	sort.Strings(options)
	return options
}

func DownloadAndExtractTarball(ctx context.Context, archiveURL, destDir string, progress func(DownloadProgress)) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return err
	}
	response, err := androidAssetHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download failed: %s", response.Status)
	}
	if progress != nil {
		progress(DownloadProgress{Phase: "downloading", Total: response.ContentLength})
	}
	gzipReader, err := gzip.NewReader(&assetProgressReader{reader: response.Body, total: response.ContentLength, progress: progress})
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	if progress != nil {
		progress(DownloadProgress{Phase: "extracting", Total: response.ContentLength})
	}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, header.Name)
		cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
		cleanTarget := filepath.Clean(target)
		if !strings.HasPrefix(cleanTarget, cleanDest) && cleanTarget != filepath.Clean(destDir) {
			return fmt.Errorf("refusing to extract %s outside destination", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tarReader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

type assetProgressReader struct {
	reader   io.Reader
	read     int64
	total    int64
	progress func(DownloadProgress)
}

func (p *assetProgressReader) Read(buffer []byte) (int, error) {
	n, err := p.reader.Read(buffer)
	p.read += int64(n)
	if n > 0 && p.progress != nil {
		p.progress(DownloadProgress{Phase: "downloading", Bytes: p.read, Total: p.total})
	}
	return n, err
}
