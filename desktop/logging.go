package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	desktopLogMaxBytes = 5 << 20
	desktopLogBackups  = 3
)

var (
	jsonSecretPattern   = regexp.MustCompile(`(?i)("(?:nonce|token|authorization|cookie|password|secret)"\s*:\s*)"(?:\\.|[^"\\])*"`)
	headerSecretPattern = regexp.MustCompile(`(?im)(\b(?:Authorization|Proxy-Authorization|Cookie|Set-Cookie)\s*:\s*)[^\r\n]*`)
	keySecretPattern    = regexp.MustCompile(`(?i)\b(nonce|token|authorization|cookie|password|secret)(\s*[=:]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:Basic|Bearer)\s+[^\s&,;]+|[^\s&,;]+)`)
	bearerPattern       = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
)

type desktopLogger struct {
	writer *rotatingWriter
}

func newDesktopLogger(dataDir string) (*desktopLogger, error) {
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create desktop log directory: %w", err)
	}
	writer, err := newRotatingWriter(filepath.Join(logDir, "app.log"), desktopLogMaxBytes, desktopLogBackups)
	if err != nil {
		return nil, err
	}
	return &desktopLogger{writer: writer}, nil
}

func (l *desktopLogger) Write(data []byte) (int, error) {
	return l.writer.Write(data)
}

func (l *desktopLogger) Close() error {
	return l.writer.Close()
}

func (l *desktopLogger) Print(message string)   { l.write("PRINT", message) }
func (l *desktopLogger) Trace(message string)   { l.write("TRACE", message) }
func (l *desktopLogger) Debug(message string)   { l.write("DEBUG", message) }
func (l *desktopLogger) Info(message string)    { l.write("INFO", message) }
func (l *desktopLogger) Warning(message string) { l.write("WARN", message) }
func (l *desktopLogger) Error(message string)   { l.write("ERROR", message) }

func (l *desktopLogger) Fatal(message string) {
	l.write("FATAL", message)
	_ = l.Close()
	os.Exit(1)
}

func (l *desktopLogger) write(level, message string) {
	message = strings.TrimRight(message, "\r\n")
	_, _ = fmt.Fprintf(l.writer, "%s %-5s %s\n", time.Now().UTC().Format(time.RFC3339Nano), level, message)
}

type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
	closed   bool
}

func newRotatingWriter(path string, maxBytes int64, backups int) (*rotatingWriter, error) {
	if maxBytes < 1 || backups < 1 {
		return nil, fmt.Errorf("invalid log rotation limits")
	}
	writer := &rotatingWriter{path: path, maxBytes: maxBytes, backups: backups}
	if err := writer.open(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *rotatingWriter) Write(data []byte) (int, error) {
	redacted := []byte(redactLogText(string(data)))
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(redacted)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(redacted)
	w.size += int64(written)
	if err != nil {
		return 0, err
	}
	// io.Writer reports consumption of the caller's input. Redaction may change
	// the byte length written to disk but consumed the complete message.
	return len(data), nil
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

func (w *rotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open desktop log: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure desktop log permissions: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat desktop log: %w", err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close desktop log for rotation: %w", err)
	}
	reopened := false
	defer func() {
		if !reopened {
			_ = w.open()
		}
	}()
	for index := w.backups; index >= 1; index-- {
		destination := fmt.Sprintf("%s.%d", w.path, index)
		if index == w.backups {
			if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove oldest desktop log: %w", err)
			}
		}
		source := w.path
		if index > 1 {
			source = fmt.Sprintf("%s.%d", w.path, index-1)
		}
		if err := os.Rename(source, destination); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate desktop log: %w", err)
		}
	}
	if err := w.open(); err != nil {
		return err
	}
	reopened = true
	return nil
}

func redactLogText(message string) string {
	message = jsonSecretPattern.ReplaceAllString(message, `$1"[REDACTED]"`)
	message = headerSecretPattern.ReplaceAllString(message, `$1[REDACTED]`)
	message = keySecretPattern.ReplaceAllString(message, `$1$2[REDACTED]`)
	return bearerPattern.ReplaceAllString(message, "Bearer [REDACTED]")
}
