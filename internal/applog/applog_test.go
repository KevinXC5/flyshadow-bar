package applog

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerRotationAndTail(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "flyshadow-bar.log")

	l, err := newLogger(logPath, 160, 2, nil)
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	defer l.Close()

	for i := 1; i <= 15; i++ {
		line := fmt.Sprintf("2026/11/25 12:00:%02d line-%02d payload-abcdefg\n", i, i)
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// 验证已触发轮转并保留了 .1 备份
	if lines, err := ReadTail(logPath+".1", 20); err != nil || len(lines) == 0 {
		t.Fatalf("expected rotated backup .1 to exist and be non-empty, err=%v lines=%d", err, len(lines))
	}

	tail, err := ReadTail(logPath, 2)
	if err != nil {
		t.Fatalf("ReadTail: %v", err)
	}
	if len(tail) == 0 || !strings.Contains(tail[len(tail)-1], "line-15") {
		t.Fatalf("expected tail to end with line-15, got %v", tail)
	}
}

func TestSanitizeRemovesSecrets(t *testing.T) {
	in := "GET https://api.example.invalid/subscribe?password=my-secret-pw&foo=1 via http://alice:superpass@127.0.0.1:7890 Authorization: Bearer abcdef1234567890"
	got := Sanitize(in)
	for _, secret := range []string{"my-secret-pw", "superpass", "abcdef1234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Sanitize leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "foo=1") || !strings.Contains(got, "127.0.0.1:7890") {
		t.Fatalf("Sanitize stripped non-secret info: %q", got)
	}
}
