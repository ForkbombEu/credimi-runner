package runtime

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Values map[string]string

type Store struct {
	Path         string
	Values       Values
	UnknownLines []string
	exists       bool
}

var validEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func DefaultConfigDir() string {
	if dir := strings.TrimSpace(os.Getenv("CREDIMI_RUNNER_CONFIG_DIR")); dir != "" {
		return dir
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, ".config", "credimi", "runner")
	}
	return filepath.Join(configDir, "credimi", "runner")
}

func LoadStore(configDir string) (*Store, error) {
	if strings.TrimSpace(configDir) == "" {
		configDir = DefaultConfigDir()
	}

	store := &Store{
		Path:   filepath.Join(configDir, ".env"),
		Values: DefaultValues(),
	}

	file, err := os.Open(store.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	defer file.Close()

	store.exists = true
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			store.UnknownLines = append(store.UnknownLines, line)
			continue
		}

		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		if !validEnvKey.MatchString(key) {
			continue
		}

		value := unquote(strings.TrimSpace(rawValue))
		if _, known := KnownKeys[key]; known {
			store.Values[key] = value
			continue
		}
		// Keep all device-prefixed keys in Values so RuntimeConfig can report
		// malformed indexes and unsupported suffixes instead of silently
		// treating them as user-managed lines.
		if strings.HasPrefix(key, "CREDIMI_DEVICE_") {
			store.Values[key] = value
			continue
		}
		store.UnknownLines = append(store.UnknownLines, key+"="+quote(value))
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *Store) Save(values Values) error {
	snapshot := cloneValues(values)
	var lines []string
	for _, key := range SortedKnownKeys() {
		lines = append(lines, key+"="+quote(snapshot[key]))
	}
	for _, line := range s.UnknownLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}

	content := strings.Join(lines, "\n") + "\n"
	tmpPath := s.Path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return err
	}

	s.Values = snapshot
	s.exists = true
	return nil
}

func (s *Store) Snapshot() Values {
	return cloneValues(s.Values)
}

func (s *Store) Exists() bool {
	return s.exists
}

func cloneValues(values Values) Values {
	cloned := make(Values, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func SortedKnownKeys() []string {
	keys := make([]string, 0, len(KnownKeys))
	for key := range KnownKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func quote(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t#\"'") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
		value = strings.ReplaceAll(value, `\"`, `"`)
	}
	return value
}
