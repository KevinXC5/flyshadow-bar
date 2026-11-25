package applog

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
)

const (
	// DefaultMaxFileBytes 单个日志文件体积上限（5 MiB），超过后自动轮转。
	DefaultMaxFileBytes int64 = 5 << 20
	// DefaultMaxBackups 最多保留的历史滚动日志份数。
	DefaultMaxBackups = 3
)

var (
	// 兜底脱敏规则：防止第三方库或错误堆栈意外带出凭据查询参数、代理密码或 Bearer Token。
	reQuerySecret = regexp.MustCompile(`(?i)([?&](?:password|token|secret|subscribe_password|subscribe-password)=)([^&\s"']+)`)
	reURLAuth     = regexp.MustCompile(`(https?://|socks5h?://)([^:@\s/]+):([^@\s/]+)@`)
	reBearerToken = regexp.MustCompile(`(?i)(Bearer\s+)([A-Za-z0-9._\-]{8,})`)
)

// Options 配置日志初始化参数。
type Options struct {
	FilePath     string
	MaxFileBytes int64
	MaxBackups   int
	MirrorStderr bool
}

// Logger 管理带自动轮转和敏感信息兜底脱敏的落盘日志。
type Logger struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	mirror   io.Writer
	file     *os.File
	size     int64
	closed   bool
}

var (
	globalMu     sync.RWMutex
	globalLogger *Logger
	globalPath   string
)

// Init 初始化全局程序日志并接管标准库 log 输出。
func Init(opts Options) (*Logger, error) {
	path := strings.TrimSpace(opts.FilePath)
	if path == "" {
		return nil, fmt.Errorf("日志文件路径不能为空")
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	backups := opts.MaxBackups
	if backups <= 0 {
		backups = DefaultMaxBackups
	}

	var mirror io.Writer
	if opts.MirrorStderr {
		mirror = os.Stderr
	}

	l, err := newLogger(path, maxBytes, backups, mirror)
	if err != nil {
		return nil, err
	}

	globalMu.Lock()
	old := globalLogger
	globalLogger = l
	globalPath = path
	globalMu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.SetOutput(l)
	return l, nil
}

// CurrentPath 返回当前已初始化的日志文件路径（未初始化时返回空字符串）。
func CurrentPath() string {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalPath
}

// Close 关闭当前全局日志文件句柄。
func Close() {
	globalMu.Lock()
	l := globalLogger
	globalLogger = nil
	globalMu.Unlock()
	if l != nil {
		_ = l.Close()
	}
}

// RecoverAndLog 捕获顶层 panic 并将完整调用栈写入程序日志，避免 GUI 模式静默闪退。
func RecoverAndLog(contextName string) {
	if r := recover(); r != nil {
		log.Printf("[FATAL] %s 发生未捕获异常: %v\n%s", contextName, r, string(debug.Stack()))
		Close()
		panic(r)
	}
}

// Sanitize 对日志文本执行兜底脱敏，屏蔽常见凭据与代理鉴权密码。
func Sanitize(s string) string {
	if s == "" {
		return s
	}
	s = reQuerySecret.ReplaceAllString(s, "${1}***")
	s = reURLAuth.ReplaceAllString(s, "${1}${2}:***@")
	s = reBearerToken.ReplaceAllString(s, "${1}***")
	return s
}

func newLogger(path string, maxBytes int64, backups int, mirror io.Writer) (*Logger, error) {
	l := &Logger{
		path:     path,
		maxBytes: maxBytes,
		backups:  backups,
		mirror:   mirror,
	}
	if err := l.openFileLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) Path() string {
	return l.path
}

// Write 实现 io.Writer，自动完成兜底脱敏、体积检查轮转与安全镜像到 stderr。
func (l *Logger) Write(p []byte) (int, error) {
	origLen := len(p)
	if origLen == 0 {
		return 0, nil
	}

	sanitized := []byte(Sanitize(string(p)))

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return origLen, nil
	}

	if l.file == nil {
		if err := l.openFileLocked(); err != nil {
			// 文件无法打开时尝试退回 stderr，尽量不丢信息
			if l.mirror != nil {
				_, _ = l.mirror.Write(sanitized)
			}
			return origLen, nil
		}
	}

	if l.size+int64(len(sanitized)) > l.maxBytes && l.size > 0 {
		_ = l.rotateLocked()
	}

	if l.file != nil {
		n, err := l.file.Write(sanitized)
		l.size += int64(n)
		if err != nil {
			// 句柄异常时下次写入自动重开
			_ = l.file.Close()
			l.file = nil
		}
	}

	// Windows -H=windowsgui 下 stderr 句柄可能不可写，忽略其写入错误
	if l.mirror != nil {
		_, _ = l.mirror.Write(sanitized)
	}

	return origLen, nil
}

// Close 关闭底层文件。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

func (l *Logger) openFileLocked() error {
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败 (%s): %w", dir, err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开日志文件失败 (%s): %w", l.path, err)
	}
	var size int64
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	l.file = f
	l.size = size
	return nil
}

func (l *Logger) rotateLocked() error {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}

	// 删除最旧的备份，并依次向后滚动 .1 -> .2 -> ...
	oldest := fmt.Sprintf("%s.%d", l.path, l.backups)
	_ = os.Remove(oldest)
	for i := l.backups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		dst := fmt.Sprintf("%s.%d", l.path, i+1)
		_ = os.Rename(src, dst)
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
		// Windows 若偶发重命名受阻则退回截断，防止单文件无限膨胀
		if f, oerr := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); oerr == nil {
			l.file = f
			l.size = 0
			return nil
		}
		return err
	}

	return l.openFileLocked()
}

// ReadTail 读取指定日志文件的最后 maxLines 行（maxLines <= 0 时默认 100）。
func ReadTail(path string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		maxLines = 100
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 单个日志文件最大约 5 MiB，顺扫环形缓冲足够快且避免 UTF-8 截断
	ring := make([]string, maxLines)
	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		ring[count%maxLines] = scanner.Text()
		count++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if count == 0 {
		return []string{}, nil
	}
	if count <= maxLines {
		out := make([]string, count)
		copy(out, ring[:count])
		return out, nil
	}
	start := count % maxLines
	out := make([]string, 0, maxLines)
	out = append(out, ring[start:]...)
	out = append(out, ring[:start]...)
	return out, nil
}
