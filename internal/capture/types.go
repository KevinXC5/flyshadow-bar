package capture

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	// DefaultMaxEntries 是内存环形缓冲默认保留的最大抓包记录条数（不落盘）。
	DefaultMaxEntries = 500
	// DefaultMaxBodyBytes 是单条请求/响应 Body 的最大捕获字节数，超出部分截断防内存膨胀。
	DefaultMaxBodyBytes = 256 * 1024
	// maxUDPSamplesPerEntry 是单条 UDP 会话保留的最近数据报样本上限。
	maxUDPSamplesPerEntry = 16
	// maxUDPSamplePreviewBytes 是单条 UDP 数据报样本保留的前缀字节上限。
	maxUDPSamplePreviewBytes = 128
	// maxDNSRecordsPerEntry 是单条 UDP DNS 会话保留的最大记录数。
	maxDNSRecordsPerEntry = 24
)

// EntryKind 标识抓包条目的大类协议。
type EntryKind string

const (
	KindHTTP  EntryKind = "http"
	KindHTTPS EntryKind = "https"
	KindTCP   EntryKind = "tcp"
	KindUDP   EntryKind = "udp"
)

// KV 表示有序的键值对（Query 参数或 HTTP Header）。
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DNSRecordSummary 保存从 UDP DNS 报文中解析出的问答摘要。
type DNSRecordSummary struct {
	Timestamp time.Time `json:"timestamp"`
	Direction string    `json:"direction"` // query | answer
	Name      string    `json:"name"`
	QType     string    `json:"qtype"`
	RCode     string    `json:"rcode,omitempty"`
	Answers   []string  `json:"answers,omitempty"`
}

// QUICMetadata 保存从 UDP QUIC Initial 报文提取出的握手元信息。
type QUICMetadata struct {
	Version string `json:"version"`
	SNI     string `json:"sni,omitempty"`
	ALPN    string `json:"alpn,omitempty"`
}

// UDPPacketSample 记录单个 UDP 数据报的精简预览样本。
type UDPPacketSample struct {
	Timestamp time.Time `json:"timestamp"`
	Direction string    `json:"direction"` // out | in
	Target    string    `json:"target,omitempty"`
	Size      int       `json:"size"`
	Summary   string    `json:"summary,omitempty"`
	Preview   []byte    `json:"preview,omitempty"`
}

// Entry 表示单条抓包记录（一次 HTTP/HTTPS 请求，或一条未解密 TLS/TCP/UDP 会话）。
type Entry struct {
	ID          uint64    `json:"id"`
	Kind        EntryKind `json:"kind"`
	Protocol    string    `json:"protocol"` // HTTP/1.1, HTTPS, TLS, TCP, DNS, QUIC, STUN, NTP, UDP
	StartedAt   time.Time `json:"startedAt"`
	DurationMs  int64     `json:"durationMs"`
	Active      bool      `json:"active"`
	ClientAddr  string    `json:"clientAddr,omitempty"`
	ProcessName string    `json:"processName,omitempty"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`

	// HTTP / HTTPS 请求级详情
	Method            string `json:"method,omitempty"`
	URL               string `json:"url,omitempty"`
	Path              string `json:"path,omitempty"`
	QueryParams       []KV   `json:"queryParams,omitempty"`
	StatusCode        int    `json:"statusCode,omitempty"`
	StatusText        string `json:"statusText,omitempty"`
	MITMDecrypted     bool   `json:"mitmDecrypted,omitempty"`
	ReqHeaders        []KV   `json:"reqHeaders,omitempty"`
	RespHeaders       []KV   `json:"respHeaders,omitempty"`
	ReqBody           []byte `json:"reqBody,omitempty"`
	ReqBodySize       int64  `json:"reqBodySize,omitempty"`
	ReqBodyTruncated  bool   `json:"reqBodyTruncated,omitempty"`
	RespBody          []byte `json:"respBody,omitempty"`
	RespBodySize      int64  `json:"respBodySize,omitempty"`
	RespBodyTruncated bool   `json:"respBodyTruncated,omitempty"`
	ReqContentType    string `json:"reqContentType,omitempty"`
	RespContentType   string `json:"respContentType,omitempty"`

	// TLS 元信息（未解密透传与已解密均尽量填充）
	TLSVersion string `json:"tlsVersion,omitempty"`
	TLSSNI     string `json:"tlsSni,omitempty"`
	TLSALPN    string `json:"tlsAlpn,omitempty"`

	// 字节统计与错误信息
	BytesSent int64  `json:"bytesSent"`
	BytesRecv int64  `json:"bytesRecv"`
	Error     string `json:"error,omitempty"`

	// UDP 专属采集信息
	UDPProtocol string             `json:"udpProtocol,omitempty"`
	PacketsSent int64              `json:"packetsSent,omitempty"`
	PacketsRecv int64              `json:"packetsRecv,omitempty"`
	DNSRecords  []DNSRecordSummary `json:"dnsRecords,omitempty"`
	QUICInfo    *QUICMetadata      `json:"quicInfo,omitempty"`
	UDPSamples  []UDPPacketSample  `json:"udpSamples,omitempty"`
}

// TargetAddr 返回规范化的 host:port 展示文本。
func (e Entry) TargetAddr() string {
	host := strings.TrimSpace(e.Host)
	if host == "" {
		host = strings.TrimSpace(e.TLSSNI)
	}
	if e.Port > 0 && host != "" {
		return net.JoinHostPort(host, fmt.Sprintf("%d", e.Port))
	}
	return host
}

// DisplaySummary 返回列表行展示的简明主路径或会话摘要。
func (e Entry) DisplaySummary() string {
	switch e.Kind {
	case KindHTTP, KindHTTPS:
		if e.Path != "" {
			return e.Path
		}
		if e.URL != "" {
			return e.URL
		}
		return e.TargetAddr()
	case KindUDP:
		if len(e.DNSRecords) > 0 {
			last := e.DNSRecords[len(e.DNSRecords)-1]
			if len(last.Answers) > 0 {
				return fmt.Sprintf("%s %s -> %s", last.QType, last.Name, strings.Join(last.Answers, ", "))
			}
			return fmt.Sprintf("%s %s", last.QType, last.Name)
		}
		if e.QUICInfo != nil && e.QUICInfo.SNI != "" {
			if e.QUICInfo.ALPN != "" {
				return fmt.Sprintf("%s (%s · %s)", e.QUICInfo.SNI, e.QUICInfo.Version, e.QUICInfo.ALPN)
			}
			return fmt.Sprintf("%s (%s)", e.QUICInfo.SNI, e.QUICInfo.Version)
		}
		return e.TargetAddr()
	default:
		if e.TLSSNI != "" && !strings.EqualFold(e.TLSSNI, e.Host) {
			return fmt.Sprintf("%s (%s)", e.TargetAddr(), e.TLSSNI)
		}
		return e.TargetAddr()
	}
}

func cloneEntry(e *Entry) Entry {
	if e == nil {
		return Entry{}
	}
	cp := *e
	if len(e.QueryParams) > 0 {
		cp.QueryParams = append([]KV(nil), e.QueryParams...)
	}
	if len(e.ReqHeaders) > 0 {
		cp.ReqHeaders = append([]KV(nil), e.ReqHeaders...)
	}
	if len(e.RespHeaders) > 0 {
		cp.RespHeaders = append([]KV(nil), e.RespHeaders...)
	}
	if len(e.ReqBody) > 0 {
		cp.ReqBody = append([]byte(nil), e.ReqBody...)
	}
	if len(e.RespBody) > 0 {
		cp.RespBody = append([]byte(nil), e.RespBody...)
	}
	if len(e.DNSRecords) > 0 {
		cp.DNSRecords = make([]DNSRecordSummary, len(e.DNSRecords))
		for i, r := range e.DNSRecords {
			cp.DNSRecords[i] = r
			if len(r.Answers) > 0 {
				cp.DNSRecords[i].Answers = append([]string(nil), r.Answers...)
			}
		}
	}
	if e.QUICInfo != nil {
		q := *e.QUICInfo
		cp.QUICInfo = &q
	}
	if len(e.UDPSamples) > 0 {
		cp.UDPSamples = make([]UDPPacketSample, len(e.UDPSamples))
		for i, s := range e.UDPSamples {
			cp.UDPSamples[i] = s
			if len(s.Preview) > 0 {
				cp.UDPSamples[i].Preview = append([]byte(nil), s.Preview...)
			}
		}
	}
	return cp
}

// Store 是线程安全的纯内存抓包环形缓冲区，不落盘以保护隐私与性能。
type Store struct {
	mu         sync.RWMutex
	maxEntries int
	paused     bool
	nextID     uint64
	entries    []*Entry
	byID       map[uint64]*Entry
	version    atomic.Uint64

	subMu     sync.Mutex
	listeners map[int]func()
	nextSubID int
}

// NewStore 创建内存抓包仓库。
func NewStore(maxEntries int) *Store {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Store{
		maxEntries: maxEntries,
		entries:    make([]*Entry, 0, min(maxEntries, 128)),
		byID:       make(map[uint64]*Entry, min(maxEntries, 128)),
		listeners:  make(map[int]func()),
	}
}

// Version 返回单调递增的数据版本号，便于 UI 按需刷新。
func (s *Store) Version() uint64 {
	if s == nil {
		return 0
	}
	return s.version.Load()
}

// Subscribe 注册数据变更回调。
func (s *Store) Subscribe(fn func()) func() {
	if s == nil || fn == nil {
		return func() {}
	}
	s.subMu.Lock()
	s.nextSubID++
	id := s.nextSubID
	s.listeners[id] = fn
	s.subMu.Unlock()
	return func() {
		s.subMu.Lock()
		delete(s.listeners, id)
		s.subMu.Unlock()
	}
}

func (s *Store) notify() {
	s.version.Add(1)
	s.subMu.Lock()
	fns := make([]func(), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.subMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// SetPaused 控制是否暂停记录新条目（暂停时仍保持代理转发不受影响）。
func (s *Store) SetPaused(paused bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	changed := s.paused != paused
	s.paused = paused
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// Paused 返回当前是否处于暂停录制状态。
func (s *Store) Paused() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paused
}

// Add 追加一条新记录并返回其唯一 ID；若处于暂停状态返回 0。
func (s *Store) Add(entry Entry) uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	if s.paused {
		s.mu.Unlock()
		return 0
	}
	s.nextID++
	entry.ID = s.nextID
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now()
	}
	cp := cloneEntry(&entry)
	ptr := &cp
	s.entries = append(s.entries, ptr)
	s.byID[ptr.ID] = ptr

	if len(s.entries) > s.maxEntries {
		evicted := s.entries[0]
		s.entries = s.entries[1:]
		delete(s.byID, evicted.ID)
	}
	s.mu.Unlock()
	s.notify()
	return ptr.ID
}

// Update 原地更新指定 ID 的记录（如补全响应信息、累计流量、追加 UDP 样本）。
func (s *Store) Update(id uint64, fn func(*Entry)) bool {
	if s == nil || id == 0 || fn == nil {
		return false
	}
	s.mu.Lock()
	e, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	fn(e)
	s.mu.Unlock()
	s.notify()
	return true
}

// Clear 清空所有内存抓包记录。
func (s *Store) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = s.entries[:0]
	clear(s.byID)
	s.mu.Unlock()
	s.notify()
}

// Len 返回当前内存中的抓包记录数。
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Snapshot 返回按时间倒序（最新在前）的记录副本快照。
func (s *Store) Snapshot() []Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make([]Entry, len(s.entries))
	for i := range s.entries {
		out[len(s.entries)-1-i] = cloneEntry(s.entries[i])
	}
	s.mu.RUnlock()
	return out
}

// Get 按 ID 获取单条记录副本。
func (s *Store) Get(id uint64) (Entry, bool) {
	if s == nil || id == 0 {
		return Entry{}, false
	}
	s.mu.RLock()
	e, ok := s.byID[id]
	if !ok {
		s.mu.RUnlock()
		return Entry{}, false
	}
	cp := cloneEntry(e)
	s.mu.RUnlock()
	return cp, true
}

// NormalizeHostPattern 规范化白名单域名规则（小写、去空白与端口，保留 *. 前缀）。
func NormalizeHostPattern(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	if h, _, err := net.SplitHostPort(s); err == nil && h != "" {
		s = h
	}
	s = strings.Trim(s, "[]")
	s = strings.TrimSuffix(s, ".")
	return strings.TrimSpace(s)
}

// NormalizeHostPatterns 规范化并去重白名单域名规则列表。
func NormalizeHostPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(patterns))
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		n := NormalizeHostPattern(p)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// MatchMITMHost 判断目标主机是否命中 HTTPS 解密白名单。
// 支持精确匹配（github.com）、通配子域（*.github.com）及后缀（.github.com）。
func MatchMITMHost(host string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return false
	}
	h := NormalizeHostPattern(host)
	if h == "" {
		return false
	}
	for _, raw := range whitelist {
		p := NormalizeHostPattern(raw)
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:] // ".example.com"
			base := p[2:]   // "example.com"
			if h == base || strings.HasSuffix(h, suffix) {
				return true
			}
			continue
		}
		if strings.HasPrefix(p, ".") {
			base := p[1:]
			if h == base || strings.HasSuffix(h, p) {
				return true
			}
			continue
		}
		if h == p {
			return true
		}
	}
	return false
}

// FlattenHeaders 将 http.Header 转为按名称排序的 KV 切片，便于稳定展示。
func FlattenHeaders(h http.Header) []KV {
	if len(h) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KV, 0, len(keys))
	for _, k := range keys {
		for _, v := range h[k] {
			out = append(out, KV{Key: k, Value: v})
		}
	}
	return out
}

// ExtractQueryParams 解析 URL Query 参数为按 Key 排序的 KV 列表。
func ExtractQueryParams(rawQuery string) []KV {
	if rawQuery == "" {
		return nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]KV, 0, len(keys))
	for _, k := range keys {
		for _, v := range values[k] {
			out = append(out, KV{Key: k, Value: v})
		}
	}
	return out
}

// FormatBodyForView 将捕获的请求/响应 Body 转为适合在 UI 中阅读的文本。
// 若内容为 JSON 自动美化缩进；若为二进制数据则转为标准 Hex + ASCII Dump。
func FormatBodyForView(body []byte, contentType string) (text string, kindLabel string) {
	if len(body) == 0 {
		return "", "空"
	}
	ct := strings.ToLower(contentType)
	trimmed := bytes.TrimSpace(body)

	// 优先尝试 JSON 美化
	if strings.Contains(ct, "json") || (len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')) {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, trimmed, "", "  "); err == nil {
			return pretty.String(), "JSON"
		}
	}

	// 表单 URL 编码也按可读文本展示
	if strings.Contains(ct, "x-www-form-urlencoded") && utf8.Valid(body) {
		return string(body), "Form"
	}

	if isReadableText(body) {
		switch {
		case strings.Contains(ct, "html"):
			return string(body), "HTML"
		case strings.Contains(ct, "xml"):
			return string(body), "XML"
		case strings.Contains(ct, "javascript"):
			return string(body), "JS"
		case strings.Contains(ct, "css"):
			return string(body), "CSS"
		default:
			return string(body), "Text"
		}
	}

	// 二进制内容展示十六进制转储（上限 4 KiB 防止 UI 渲染卡顿）
	limit := min(len(body), 4096)
	dump := hex.Dump(body[:limit])
	if len(body) > limit {
		dump += fmt.Sprintf("\n...（共 %d 字节，已仅预览前 %d 字节）", len(body), limit)
	}
	return dump, "Hex"
}

// FormatHexPreview 将短二进制数据报格式化为紧凑 Hex + ASCII 预览。
func FormatHexPreview(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return strings.TrimRight(hex.Dump(data), "\n")
}

func isReadableText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	sample := b
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	nonPrintable := 0
	for _, c := range sample {
		if c == 0 {
			return false
		}
		if c < 0x20 && c != '\n' && c != '\r' && c != '\t' {
			nonPrintable++
		}
	}
	return nonPrintable*20 <= len(sample)
}

// BuildCURL 根据 HTTP/HTTPS 抓包记录生成可直接在终端复现的 curl 命令。
func BuildCURL(e Entry) string {
	if e.URL == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("curl")
	if e.Method != "" && e.Method != http.MethodGet {
		b.WriteString(" -X ")
		b.WriteString(shellQuote(e.Method))
	}
	b.WriteString(" ")
	b.WriteString(shellQuote(e.URL))
	for _, kv := range e.ReqHeaders {
		lower := strings.ToLower(kv.Key)
		if lower == "content-length" || lower == "host" || lower == "accept-encoding" {
			continue
		}
		b.WriteString(" -H ")
		b.WriteString(shellQuote(kv.Key + ": " + kv.Value))
	}
	if len(e.ReqBody) > 0 && isReadableText(e.ReqBody) {
		b.WriteString(" --data-raw ")
		b.WriteString(shellQuote(string(e.ReqBody)))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
