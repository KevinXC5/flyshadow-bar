package sysproxy

import (
	"reflect"
	"testing"
)

func TestParseNetworkServices(t *testing.T) {
	raw := `An asterisk (*) denotes that a network service is disabled.
Wi-Fi
*Bluetooth PAN
Thunderbolt Bridge
USB 10/100/1000 LAN
  *Disabled Ethernet

Tailscale
`
	got := parseNetworkServices(raw)
	want := []string{"Wi-Fi", "Thunderbolt Bridge", "USB 10/100/1000 LAN", "Tailscale"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("解析网络服务不匹配: got=%v, want=%v", got, want)
	}

	if empty := parseNetworkServices("An asterisk (*) denotes that a network service is disabled.\n*Wi-Fi\n"); len(empty) != 0 {
		t.Fatalf("全部禁用时应返回空切片: got=%v", empty)
	}
}

func TestParseWebProxyOutputAndMatch(t *testing.T) {
	enabledOut := `Enabled: Yes
Server: 127.0.0.1
Port: 7890
Authenticated Proxy Enabled: 0
`
	info := parseWebProxyOutput(enabledOut)
	if !info.Enabled || info.Server != "127.0.0.1" || info.Port != 7890 {
		t.Fatalf("解析已启用代理输出失败: got=%+v", info)
	}

	if !matchWebProxy(info, "127.0.0.1", 7890) {
		t.Fatalf("期望精确匹配 127.0.0.1:7890")
	}
	if matchWebProxy(info, "127.0.0.1", 7891) {
		t.Fatalf("端口不同不应匹配")
	}
	if matchWebProxy(info, "192.168.1.2", 7890) {
		t.Fatalf("主机不同不应匹配")
	}
	if !matchWebProxy(info, "", 7890) {
		t.Fatalf("主机为空且端口一致时应匹配")
	}
	if !matchWebProxy(info, "192.168.1.2", 0) {
		t.Fatalf("port<=0 时仅判断 Enabled: Yes，应返回 true")
	}

	disabledOut := `Enabled: No
Server: 127.0.0.1
Port: 7890
Authenticated Proxy Enabled: 0
`
	disabledInfo := parseWebProxyOutput(disabledOut)
	if disabledInfo.Enabled {
		t.Fatalf("解析已禁用代理输出失败: got=%+v", disabledInfo)
	}
	if matchWebProxy(disabledInfo, "127.0.0.1", 7890) || matchWebProxy(disabledInfo, "", 0) {
		t.Fatalf("未启用时不应匹配")
	}
}

func TestMatchWindowsProxyServer(t *testing.T) {
	cases := []struct {
		name   string
		server string
		host   string
		port   int
		want   bool
	}{
		{name: "标准 host:port 匹配", server: "127.0.0.1:7890", host: "127.0.0.1", port: 7890, want: true},
		{name: "标准 host:port 端口不匹配", server: "127.0.0.1:7890", host: "127.0.0.1", port: 7891, want: false},
		{name: "分协议格式匹配", server: "http=127.0.0.1:7890;https=127.0.0.1:7890", host: "127.0.0.1", port: 7890, want: true},
		{name: "带协议前缀匹配", server: "http://127.0.0.1:7890", host: "127.0.0.1", port: 7890, want: true},
		{name: "仅指定端口匹配", server: "127.0.0.1:7890", host: "", port: 7890, want: true},
		{name: "port<=0 放行", server: "", host: "127.0.0.1", port: 0, want: true},
		{name: "空值不匹配", server: "", host: "127.0.0.1", port: 7890, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchWindowsProxyServer(tc.server, tc.host, tc.port); got != tc.want {
				t.Fatalf("matchWindowsProxyServer(%q, %q, %d) = %v, want %v", tc.server, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestValidateProxyTarget(t *testing.T) {
	if _, err := validateProxyTarget("", 7890); err == nil {
		t.Fatalf("空主机应返回错误")
	}
	if _, err := validateProxyTarget("127.0.0.1", 0); err == nil {
		t.Fatalf("端口 0 应返回错误")
	}
	if _, err := validateProxyTarget("127.0.0.1", 70000); err == nil {
		t.Fatalf("端口越界应返回错误")
	}
	host, err := validateProxyTarget(" 127.0.0.1 ", 7890)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("合法参数校验失败: host=%q, err=%v", host, err)
	}
}
