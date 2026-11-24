package core

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

// ProxyMode 对应底层核心库 set_proxy_type 的枚举值。
type ProxyMode int32

const (
	ProxyModeDirect ProxyMode = 0
	ProxyModeReject ProxyMode = 1
	ProxyModeGlobal ProxyMode = 2
	ProxyModeRule   ProxyMode = 3
)

// String 返回模式英文标识。
func (m ProxyMode) String() string {
	switch m {
	case ProxyModeDirect:
		return "direct"
	case ProxyModeReject:
		return "reject"
	case ProxyModeGlobal:
		return "global"
	case ProxyModeRule:
		return "rule"
	default:
		return "rule"
	}
}

// Label 返回中文展示名。
func (m ProxyMode) Label() string {
	switch m {
	case ProxyModeDirect:
		return "直连模式"
	case ProxyModeReject:
		return "拒绝"
	case ProxyModeGlobal:
		return "全局模式"
	case ProxyModeRule:
		return "规则模式"
	default:
		return "规则模式"
	}
}

// ParseProxyMode 解析模式字符串。
func ParseProxyMode(s string) (ProxyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rule", "rules", "规则", "3":
		return ProxyModeRule, nil
	case "global", "全局", "2":
		return ProxyModeGlobal, nil
	case "direct", "直连", "0":
		return ProxyModeDirect, nil
	default:
		return ProxyModeRule, fmt.Errorf("不支持的代理模式 %q（支持: rule, global, direct）", s)
	}
}

// TransportDecision 表示基于全局开关与节点能力综合计算出的传输层参数。
type TransportDecision struct {
	EnableQuic      bool `json:"enableQuic"`
	EnableNativeUDP bool `json:"enableNativeUdp"`
}

// ResolveTransportDecision 按官方 NodeTransportConfigKt.resolveNodeTransportDecision 规则判定。
func ResolveTransportDecision(udpAccelerationEnabled, nativeUDPEnabled bool, node config.Node) TransportDecision {
	return TransportDecision{
		EnableQuic:      udpAccelerationEnabled && node.SupportsQuic(),
		EnableNativeUDP: nativeUDPEnabled && node.SupportsNativeUDP(),
	}
}

// ConnectionInfo 对应核心库 get_tunnel_mapper_info_json 返回的单条连接监控记录。
type ConnectionInfo struct {
	Protocol             string `json:"protocol"`
	SourceAddr           string `json:"source_addr"`
	SourcePort           int    `json:"source_port"`
	TargetAddr           string `json:"target_addr"`
	TargetPort           int    `json:"target_port"`
	MatcherName          string `json:"matcher_name"`
	MatcherRule          string `json:"matcher_rule"`
	ProcessName          string `json:"process_name"`
	ProxyType            string `json:"proxy_type"`
	ActiveTime           int64  `json:"active_time"`
	UploadTraffic        int64  `json:"upload_traffic"`
	DownloadTraffic      int64  `json:"download_traffic"`
	UploadTrafficTotal   int64  `json:"upload_traffic_total"`
	DownloadTrafficTotal int64  `json:"download_traffic_total"`
	Status               string `json:"status"`
}

// CloseKey 生成传给核心库 close_connect 的精确连接键（与官方客户端完全一致）。
func (c ConnectionInfo) CloseKey() string {
	return fmt.Sprintf("%s:%d===%s", c.SourceAddr, c.SourcePort, c.Protocol)
}

// Client 抽象底层 FlyShadow 原生动态库能力，便于单测使用 Mock 替换。
type Client interface {
	LibPath() string
	Version() string

	Init() error
	SetClientUUID(uuid string)
	SetProxyType(mode ProxyMode)
	SetUDPProxyType(mode ProxyMode)
	SetNativeUDP(enabled bool)
	SetIPv6Enable(enabled bool)
	SetUseBuildInDNS(enabled bool)
	SetDirectConnPriority(enabled bool, timeoutMs int64)

	ClearDomainRules()
	SetDomainRule(domain string, matching, proxyType, directConnPriority int32)
	SetGeoIP(country string, data []byte)

	StartProxy(port int, allowLAN bool, username, password string) error
	StopProxy()

	ConnectTunnel(host string, port int, password string, enableQuic bool) error
	CloseTunnel() int32

	TestTunnelDelay(node config.Node, enableQuic bool) int64
	TunnelUploadBytes() int64
	TunnelDownloadBytes() int64
	TunnelPingDelay() int32
	// TunnelStatus 返回底层核心库状态码：0 表示已连接成功。
	TunnelStatus() int32
	TunnelConnections() ([]ConnectionInfo, error)
	CloseConnection(key string)

	Close()
}

// delayTestPayload 是 test_tunnel_delay 所需的节点 JSON 载荷。
type delayTestPayload struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

func marshalDelayPayload(node config.Node) (string, error) {
	b, err := json.Marshal(delayTestPayload{
		Host:     node.Host,
		Port:     node.Port,
		Password: node.Password,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
