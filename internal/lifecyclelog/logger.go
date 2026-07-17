package lifecyclelog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxBytes = 5 << 20
	DefaultBackups  = 3
)

type Options struct {
	MaxBytes int
	Backups  int
	Now      func() time.Time
}

// Logger writes only controller lifecycle events. It deliberately has no
// method accepting command output or an environment slice.
type Logger struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	writer  *bufio.Writer
	max     int64
	backups int
	now     func() time.Time
	size    int64
}

func New(path string, options Options) (*Logger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("lifecycle log path is empty")
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.Backups < 0 {
		options.Backups = DefaultBackups
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lifecycle log directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink lifecycle log: %s", path)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lifecycle log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect lifecycle log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat lifecycle log: %w", err)
	}
	return &Logger{
		path: path, file: file, writer: bufio.NewWriter(file),
		max: int64(options.MaxBytes), backups: options.Backups,
		now: options.Now, size: info.Size(),
	}, nil
}

func (l *Logger) Emit(event Event) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("lifecycle logger is closed")
	}
	if event.Schema == 0 {
		event.Schema = SchemaVersion
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = l.now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	event.Fields = sanitizeFields(event.Fields)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode lifecycle event: %w", err)
	}
	payload = append(payload, '\n')
	if l.size > 0 && l.size+int64(len(payload)) > l.max {
		if err := l.rotateLocked(); err != nil {
			return err
		}
	}
	if _, err := l.writer.Write(payload); err != nil {
		return fmt.Errorf("write lifecycle event: %w", err)
	}
	if err := l.writer.Flush(); err != nil {
		return fmt.Errorf("flush lifecycle event: %w", err)
	}
	l.size += int64(len(payload))
	return nil
}

func (l *Logger) Sync() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	if err := l.writer.Flush(); err != nil {
		return err
	}
	return l.file.Sync()
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	flushErr := l.writer.Flush()
	syncErr := l.file.Sync()
	closeErr := l.file.Close()
	l.file = nil
	l.writer = nil
	return errors.Join(flushErr, syncErr, closeErr)
}

func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Logger) rotateLocked() error {
	if err := l.writer.Flush(); err != nil {
		return fmt.Errorf("flush lifecycle log before rotation: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close lifecycle log before rotation: %w", err)
	}
	for index := l.backups; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", l.path, index)
		to := fmt.Sprintf("%s.%d", l.path, index+1)
		if index == l.backups {
			_ = os.Remove(to)
		}
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate lifecycle log %s: %w", from, err)
		}
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("archive lifecycle log: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reopen lifecycle log after rotation: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	l.file = file
	l.writer = bufio.NewWriter(file)
	l.size = 0
	return nil
}

func sanitizeFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if sensitiveKey(key) {
			out[key] = "<redacted>"
			continue
		}
		out[key] = sanitizeValue(key, value)
	}
	return out
}

func sanitizeValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeFields(typed)
	case []string:
		items := make([]any, len(typed))
		for i, item := range typed {
			items[i] = sanitizeValue(key, item)
		}
		return items
	case string:
		if strings.Contains(strings.ToLower(key), "url") {
			return safeURL(typed)
		}
		return typed
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	for _, token := range []string{"key", "token", "password", "secret", "authorization", "private", "credential"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func safeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "<invalid-url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
