package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

// Client 用于 CLI 子命令连接并操控正在运行的 flyshadow-bar GUI 实例。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// Discover 从默认（或指定）运行时发现文件创建控制客户端。
func Discover(runtimePath string) (*Client, error) {
	info, err := ReadRuntimeFile(runtimePath)
	if err != nil {
		return nil, fmt.Errorf("未检测到正在运行的 flyshadow-bar 实例（请先无参数启动 GUI 应用）: %w", err)
	}
	return &Client{
		baseURL: "http://" + info.Addr,
		token:   info.Token,
		// 使用直连 Transport，避免被系统/环境变量 HTTP_PROXY 劫持回环请求
		http: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				Proxy: nil,
			},
		},
	}, nil
}

func (c *Client) Status(ctx context.Context) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodGet, "/api/v1/status", nil, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) Nodes(ctx context.Context) ([]engine.NodeSnapshot, error) {
	var resp struct {
		Nodes []engine.NodeSnapshot `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/nodes", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

func (c *Client) SelectNode(ctx context.Context, query string) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/node", nodeRequest{Query: query}, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) SetMode(ctx context.Context, mode string) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/mode", modeRequest{Mode: mode}, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) SetUDP(ctx context.Context, action string) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/udp", toggleRequest{Action: action}, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) SetNativeUDP(ctx context.Context, action string) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/native-udp", toggleRequest{Action: action}, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) SetSysProxy(ctx context.Context, action string) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/sysproxy", toggleRequest{Action: action}, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) Ping(ctx context.Context) ([]engine.NodeSnapshot, error) {
	var resp struct {
		Nodes []engine.NodeSnapshot `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/ping", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Nodes, nil
}

func (c *Client) Reload(ctx context.Context) (*engine.Snapshot, error) {
	var snap engine.Snapshot
	if err := c.do(ctx, http.MethodPost, "/api/v1/reload", nil, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) Rules(ctx context.Context) ([]config.DomainRule, error) {
	var resp struct {
		Rules []config.DomainRule `json:"rules"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/rules", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Rules, nil
}

func (c *Client) AddRule(ctx context.Context, rule config.DomainRule) (*config.DomainRule, error) {
	var added config.DomainRule
	if err := c.do(ctx, http.MethodPost, "/api/v1/rules", rule, &added); err != nil {
		return nil, err
	}
	return &added, nil
}

func (c *Client) DeleteRule(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/rules", ruleIDRequest{ID: id}, nil)
}

func (c *Client) ToggleRule(ctx context.Context, id int) (bool, error) {
	var resp struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/rules/toggle", ruleIDRequest{ID: id}, &resp); err != nil {
		return false, err
	}
	return resp.Enabled, nil
}

func (c *Client) Connections(ctx context.Context) ([]core.ConnectionInfo, error) {
	var resp struct {
		Connections []core.ConnectionInfo `json:"connections"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/connections", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Connections, nil
}

func (c *Client) CloseConnection(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/connections/close", closeConnRequest{Key: key}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, dst any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接运行中的 flyshadow-bar 实例失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var errBody struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errBody) == nil && errBody.Error != "" {
			return errors.New(errBody.Error)
		}
		return fmt.Errorf("控制服务返回错误 (%d): %s", resp.StatusCode, string(data))
	}
	if dst != nil && len(data) > 0 {
		if err := json.Unmarshal(data, dst); err != nil {
			return fmt.Errorf("解析控制服务响应失败: %w", err)
		}
	}
	return nil
}
