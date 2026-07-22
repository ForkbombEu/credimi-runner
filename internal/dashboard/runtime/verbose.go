package runtime

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const verboseLogPathEnv = "CREDIMI_RUNNER_VERBOSE_LOG_PATH"

// verboseLog is deliberately separate from lifecycle.jsonl: it records raw
// Docker and container output for an explicitly requested diagnostic session.
// Its file mode is private because command output can contain sensitive data.
type verboseLog struct {
	mu   sync.Mutex
	file *os.File
}

func openVerboseLog() *verboseLog {
	path := strings.TrimSpace(os.Getenv(verboseLogPathEnv))
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return &verboseLog{file: file}
}

func (l *verboseLog) Write(data []byte) (int, error) {
	if l == nil || l.file == nil {
		return len(data), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Write(data)
}

func (l *verboseLog) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	_, _ = l.Write([]byte(fmt.Sprintf("%s runtime: %s\n", time.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))))
}

func (l *verboseLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
