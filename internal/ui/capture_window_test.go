package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/KevinXC5/flyshadow-bar/internal/capture"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
)

func TestCaptureWindowRendersHTTPAndUDPAndCertSafetyModal(t *testing.T) {
	now := time.Now()
	vm := &captureViewModel{
		udpExpanded: make(map[int]bool),
		status: engine.CaptureStatus{
			Enabled:     true,
			ProxyPort:   7890,
			MITMEnabled: true,
			MITMHosts:   []string{"api.github.com"},
			Cert: capture.CertStatus{
				Generated:         true,
				CertPath:          "/tmp/mock/flyshadow-bar-ca.crt",
				FingerprintSHA256: "ABCDEF0123456789ABCDEF0123456789",
				NotAfter:          now.AddDate(10, 0, 0),
				Installed:         false,
				Trusted:           false,
				StatusDetail:      "证书尚未安装到系统根证书库",
				InstallPromptHint: "安装时会弹出系统原生安全确认弹窗，确认后即生效。",
				EnvSnippets: []capture.EnvSnippet{
					{Label: "Node.js", Value: `export NODE_EXTRA_CA_CERTS="/tmp/mock/flyshadow-bar-ca.crt"`},
				},
			},
		},
		entries: []capture.Entry{
			{
				ID:              1,
				Kind:            capture.KindHTTPS,
				Protocol:        "HTTPS",
				StartedAt:       now,
				DurationMs:      48,
				Host:            "api.github.com",
				Port:            443,
				Method:          "POST",
				URL:             "https://api.github.com/repos/demo?page=1",
				Path:            "/repos/demo",
				QueryParams:     []capture.KV{{Key: "page", Value: "1"}},
				StatusCode:      200,
				StatusText:      "200 OK",
				MITMDecrypted:   true,
				TLSVersion:      "TLS 1.3",
				TLSSNI:          "api.github.com",
				ProcessName:     "git-remote-http",
				ReqHeaders:      []capture.KV{{Key: "Accept", Value: "application/json"}},
				RespHeaders:     []capture.KV{{Key: "Content-Type", Value: "application/json"}},
				ReqBody:         []byte(`{"hello":"world"}`),
				ReqBodySize:     17,
				ReqContentType:  "application/json",
				RespBody:        []byte(`{"ok":true}`),
				RespBodySize:    11,
				RespContentType: "application/json",
			},
			{
				ID:          2,
				Kind:        capture.KindUDP,
				Protocol:    "DNS",
				UDPProtocol: "DNS",
				StartedAt:   now,
				DurationMs:  12,
				Host:        "8.8.8.8",
				Port:        53,
				PacketsSent: 1,
				PacketsRecv: 1,
				BytesSent:   42,
				BytesRecv:   58,
				DNSRecords: []capture.DNSRecordSummary{
					{Timestamp: now, Direction: "answer", Name: "example.com", QType: "A", RCode: "NOERROR", Answers: []string{"93.184.216.34"}},
				},
				UDPSamples: []capture.UDPPacketSample{
					{Timestamp: now, Direction: "out", Size: 42, Summary: "查询 example.com (A)", Preview: []byte{0x12, 0x34, 0x01, 0x00}},
				},
			},
		},
		selectedID: 1,
	}

	tester := ui.NewTester(vm.render, 1360, 860)
	tester.Frame()

	// 验证 HTTP 已解密请求各子 Tab 及折叠 Headers 均能无 panic 正常渲染
	for tab := 0; tab < 4; tab++ {
		vm.detailTab = tab
		tester.Frame()
	}
	vm.detailTab = 3
	vm.respHeadersCollapsed = true
	tester.Frame()

	// 切换到 UDP 记录并展开样本预览
	vm.selectedID = 2
	vm.udpExpanded[0] = true
	tester.Frame()

	// 打开自签证书与白名单安全引导弹窗
	vm.certModalOpen = true
	tester.Frame()
}
