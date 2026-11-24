package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

const (
	cdnCacheTTL      = 10 * time.Minute
	requestTimeout   = 20 * time.Second
	dohTimeout       = 4 * time.Second
	maxResponseBytes = 16 << 20 // 16 MiB
)

// 与官方客户端 CdnManager 一致的 DoH TXT 发现端点与公开回退地址。
var (
	defaultDoHEndpoints = []string{
		"https://doh.pub/dns-query?name=_cdn.hkspeedup.com&type=TXT",
		"https://dns.alidns.com/resolve?name=_cdn.hkspeedup.com&type=TXT",
		"https://1.1.1.1/dns-query?name=_cdn.hkspeedup.com&type=TXT",
		"https://dns.google/resolve?name=_cdn.hkspeedup.com&type=TXT",
	}
	defaultFallbackBaseURLs = []string{
		"https://main.hkspeedup.com",
		"https://api.up-up-up.com",
	}
)

// SyncAPI 抽象官方云端订阅与自定义规则同步接口，便于引擎层与单元测试解耦。
type SyncAPI interface {
	FetchSubscribe(ctx context.Context, subscribePassword string, proxyURL string) (*config.SubscribeInfo, error)
	FetchCustomRules(ctx context.Context, subscribePassword string, proxyURL string) ([]config.DomainRule, error)
	SaveCustomRule(ctx context.Context, subscribePassword string, rule config.DomainRule, proxyURL string) error
	UpdateCustomRule(ctx context.Context, subscribePassword string, rule config.DomainRule, proxyURL string) error
	RemoveCustomRule(ctx context.Context, subscribePassword string, id int, proxyURL string) error
}

// Client 实现与 FlyShadow 原版一致的云端订阅与自定义规则双向同步客户端。
type Client struct {
	// BaseURLs 允许测试或调用方显式固定 API 地址，留空时自动通过环境变量 / DoH TXT / 官方回退列表解析。
	BaseURLs []string
	// DisableDoH 关闭 DoH 动态发现（主要用于单元测试）。
	DisableDoH bool

	mu        sync.Mutex
	cachedURL []string
	cachedAt  time.Time
}

// NewClient 创建默认云端同步客户端。
func NewClient() *Client {
	return &Client{}
}

type apiResponse struct {
	Status  string          `json:"status"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// FetchSubscribe 拉取最新订阅信息（节点列表、内置规则、流量统计、VIP 等级与到期时间）。
func (c *Client) FetchSubscribe(ctx context.Context, subscribePassword string, proxyURL string) (*config.SubscribeInfo, error) {
	pw := strings.TrimSpace(subscribePassword)
	if pw == "" {
		return nil, errors.New("未配置订阅凭据，无法同步订阅")
	}

	path := "/subscribe?password=" + url.QueryEscape(pw)
	raw, err := c.doWithFallback(ctx, http.MethodGet, path, nil, nil, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("同步订阅失败: %w", err)
	}

	var info config.SubscribeInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("解析订阅响应失败: %w", err)
	}
	if len(info.Nodes) == 0 && info.User == nil && len(info.CustomRules) == 0 {
		// 兼容错误包裹结构（例如认证失效返回 ResponseVo）
		var wrap apiResponse
		if json.Unmarshal(raw, &wrap) == nil && wrap.Code != 0 && wrap.Code != 200 {
			msg := strings.TrimSpace(wrap.Message)
			if msg == "" {
				msg = fmt.Sprintf("服务端返回错误码 %d", wrap.Code)
			}
			return nil, errors.New(msg)
		}
		return nil, errors.New("订阅响应内容为空")
	}
	return &info, nil
}

// FetchCustomRules 拉取云端保存的用户自定义分流规则列表。
func (c *Client) FetchCustomRules(ctx context.Context, subscribePassword string, proxyURL string) ([]config.DomainRule, error) {
	headers, err := authHeaders(subscribePassword)
	if err != nil {
		return nil, err
	}
	raw, err := c.doWithFallback(ctx, http.MethodPost, "/custom/rule/list", headers, nil, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("拉取云端规则失败: %w", err)
	}
	data, err := parseEnvelope(raw)
	if err != nil {
		return nil, fmt.Errorf("拉取云端规则失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return []config.DomainRule{}, nil
	}
	var rules []config.DomainRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("解析云端规则列表失败: %w", err)
	}
	if rules == nil {
		rules = []config.DomainRule{}
	}
	return rules, nil
}

// SaveCustomRule 将新增的自定义规则保存至云端。
func (c *Client) SaveCustomRule(ctx context.Context, subscribePassword string, rule config.DomainRule, proxyURL string) error {
	headers, err := authHeaders(subscribePassword)
	if err != nil {
		return err
	}
	payload := rule
	payload.ID = 0 // 新增不传 ID，由云端分配
	if payload.Enable == nil {
		payload.Enable = config.IntPtr(1)
	}
	raw, err := c.doWithFallback(ctx, http.MethodPost, "/custom/rule/save", headers, payload, proxyURL)
	if err != nil {
		return fmt.Errorf("保存云端规则失败: %w", err)
	}
	if _, err := parseEnvelope(raw); err != nil {
		return fmt.Errorf("保存云端规则失败: %w", err)
	}
	return nil
}

// UpdateCustomRule 将修改或启停后的自定义规则同步更新至云端。
func (c *Client) UpdateCustomRule(ctx context.Context, subscribePassword string, rule config.DomainRule, proxyURL string) error {
	if rule.ID <= 0 {
		return errors.New("规则 ID 无效，无法更新云端规则")
	}
	headers, err := authHeaders(subscribePassword)
	if err != nil {
		return err
	}
	if rule.Enable == nil {
		rule.Enable = config.IntPtr(1)
	}
	raw, err := c.doWithFallback(ctx, http.MethodPost, "/custom/rule/update", headers, rule, proxyURL)
	if err != nil {
		return fmt.Errorf("更新云端规则失败: %w", err)
	}
	if _, err := parseEnvelope(raw); err != nil {
		return fmt.Errorf("更新云端规则失败: %w", err)
	}
	return nil
}

// RemoveCustomRule 从云端删除指定 ID 的自定义规则。
func (c *Client) RemoveCustomRule(ctx context.Context, subscribePassword string, id int, proxyURL string) error {
	if id <= 0 {
		return errors.New("规则 ID 无效，无法删除云端规则")
	}
	headers, err := authHeaders(subscribePassword)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/custom/rule/remove/%d", id)
	raw, err := c.doWithFallback(ctx, http.MethodPost, path, headers, nil, proxyURL)
	if err != nil {
		return fmt.Errorf("删除云端规则失败: %w", err)
	}
	if _, err := parseEnvelope(raw); err != nil {
		return fmt.Errorf("删除云端规则失败: %w", err)
	}
	return nil
}

func authHeaders(subscribePassword string) (map[string]string, error) {
	pw := strings.TrimSpace(subscribePassword)
	if pw == "" {
		return nil, errors.New("未配置订阅凭据，无法同步云端规则")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(pw))
	return map[string]string{
		"subscribe-password": encoded,
	}, nil
}

func parseEnvelope(raw []byte) (json.RawMessage, error) {
	var env apiResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("响应格式无效: %w", err)
	}
	if env.Code != 200 {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = fmt.Sprintf("服务端返回错误码 %d", env.Code)
		}
		return nil, errors.New(msg)
	}
	return env.Data, nil
}

func (c *Client) doWithFallback(ctx context.Context, method, path string, headers map[string]string, body any, proxyURL string) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}

	bases := c.resolveBaseURLs(ctx)
	clients := buildHTTPClients(proxyURL)

	var lastErr error
	for _, hc := range clients {
		for _, base := range bases {
			reqURL := strings.TrimRight(base, "/") + path
			data, status, err := doOnce(ctx, hc, method, reqURL, headers, bodyBytes)
			if err != nil {
				lastErr = sanitizeNetworkErr(err)
				continue
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				return nil, fmt.Errorf("订阅凭据无效或已过期 (HTTP %d)", status)
			}
			if status >= 500 {
				lastErr = fmt.Errorf("服务端异常 (HTTP %d)", status)
				continue
			}
			if status >= 400 {
				return nil, fmt.Errorf("请求失败 (HTTP %d)", status)
			}
			return data, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("无法连接同步服务")
}

func doOnce(ctx context.Context, hc *http.Client, method, rawURL string, headers map[string]string, bodyBytes []byte) ([]byte, int, error) {
	var reader io.Reader
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "FlyShadow-Bar")
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// sanitizeNetworkErr 剥离 URL 中的敏感查询参数（如 password），避免报错信息泄露订阅凭据。
func sanitizeNetworkErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Err != nil {
			return ue.Err
		}
		return errors.New("网络请求失败")
	}
	return err
}

func buildHTTPClients(proxyURL string) []*http.Client {
	direct := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	if strings.TrimSpace(proxyURL) == "" {
		return []*http.Client{direct}
	}
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.Host == "" {
		return []*http.Client{direct}
	}
	proxied := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsed),
		},
	}
	// 已连接隧道时优先经本地代理访问，若失败自动回退直连（与原版体验一致）
	return []*http.Client{proxied, direct}
}

func (c *Client) resolveBaseURLs(ctx context.Context) []string {
	if len(c.BaseURLs) > 0 {
		return append([]string(nil), c.BaseURLs...)
	}
	if override := strings.TrimSpace(os.Getenv("FLYSHADOW_API_URL")); override != "" {
		return []string{strings.TrimRight(override, "/")}
	}

	c.mu.Lock()
	if len(c.cachedURL) > 0 && time.Since(c.cachedAt) < cdnCacheTTL {
		out := append([]string(nil), c.cachedURL...)
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()

	var urls []string
	if !c.DisableDoH {
		urls = fetchBaseURLsFromDoH(ctx)
	}
	urls = appendUniqueURLs(urls, defaultFallbackBaseURLs...)

	c.mu.Lock()
	c.cachedURL = append([]string(nil), urls...)
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return urls
}

func fetchBaseURLsFromDoH(ctx context.Context) []string {
	dohCtx, cancel := context.WithTimeout(ctx, dohTimeout)
	defer cancel()

	hc := &http.Client{
		Timeout: dohTimeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	for _, endpoint := range defaultDoHEndpoints {
		req, err := http.NewRequestWithContext(dohCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/dns-json")
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if rerr != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		if parsed := parseDoHTXTURLs(raw); len(parsed) > 0 {
			return parsed
		}
	}
	return nil
}

func parseDoHTXTURLs(raw []byte) []string {
	var doc struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []string
	for _, ans := range doc.Answer {
		if ans.Type != 16 {
			continue
		}
		cleaned := strings.ReplaceAll(strings.TrimSpace(ans.Data), `"`, "")
		for _, part := range strings.Split(cleaned, ",") {
			u := strings.TrimRight(strings.TrimSpace(part), "/")
			if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
				out = appendUniqueURLs(out, u)
			}
		}
	}
	return out
}

func appendUniqueURLs(dst []string, items ...string) []string {
	seen := make(map[string]struct{}, len(dst))
	for _, s := range dst {
		seen[s] = struct{}{}
	}
	for _, it := range items {
		u := strings.TrimRight(strings.TrimSpace(it), "/")
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		dst = append(dst, u)
	}
	return dst
}
