//go:build darwin

package sysproxy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinManagerExcludedServicesEnableAndDisable(t *testing.T) {
	var calls []string
	mgr := &darwinManager{
		run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "networksetup" && len(args) == 1 && args[0] == "-listallnetworkservices" {
				return "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB 10/100/1000 LAN\n*Bluetooth PAN\nThunderbolt Bridge\n", nil
			}
			return "", nil
		},
	}

	mgr.SetExcludedServices([]string{" USB 10/100/1000 LAN ", "USB 10/100/1000 LAN"})
	if got := mgr.ExcludedServices(); !reflect.DeepEqual(got, []string{"USB 10/100/1000 LAN"}) {
		t.Fatalf("ExcludedServices 不匹配: got=%v", got)
	}

	if err := mgr.Enable("127.0.0.1", 7890); err != nil {
		t.Fatalf("Enable 失败: %v", err)
	}

	for _, cmd := range calls {
		if strings.Contains(cmd, "USB 10/100/1000 LAN") {
			t.Fatalf("Enable 不应操作已排除网卡: %s", cmd)
		}
	}
	if !containsCall(calls, "networksetup -setwebproxy Wi-Fi 127.0.0.1 7890") {
		t.Fatalf("Enable 未给 Wi-Fi 设置代理: %v", calls)
	}
	if !containsCall(calls, "networksetup -setwebproxy Thunderbolt Bridge 127.0.0.1 7890") {
		t.Fatalf("Enable 未给 Thunderbolt Bridge 设置代理: %v", calls)
	}

	calls = nil
	if err := mgr.Disable(); err != nil {
		t.Fatalf("Disable 失败: %v", err)
	}
	for _, cmd := range calls {
		if strings.Contains(cmd, "USB 10/100/1000 LAN") {
			t.Fatalf("Disable 不应操作已排除网卡: %s", cmd)
		}
	}
	if !containsCall(calls, "networksetup -setwebproxystate Wi-Fi off") {
		t.Fatalf("Disable 未关闭 Wi-Fi 代理: %v", calls)
	}
}

func TestDarwinManagerEnableFailsWhenAllServicesExcluded(t *testing.T) {
	mgr := &darwinManager{
		run: func(name string, args ...string) (string, error) {
			if name == "networksetup" && len(args) == 1 && args[0] == "-listallnetworkservices" {
				return "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB 10/100/1000 LAN\n", nil
			}
			return "", nil
		},
	}
	mgr.SetExcludedServices([]string{"Wi-Fi", "USB 10/100/1000 LAN"})

	if err := mgr.Enable("127.0.0.1", 7890); err == nil {
		t.Fatalf("全部网卡均排除时 Enable 应返回错误")
	}
	if err := mgr.Disable(); err != nil {
		t.Fatalf("全部网卡均排除时 Disable 不应报错: %v", err)
	}
}

func TestDarwinManagerDisableServiceIfMatches(t *testing.T) {
	var disabledFlags []string
	mgr := &darwinManager{
		run: func(name string, args ...string) (string, error) {
			if name != "networksetup" || len(args) < 2 {
				return "", fmt.Errorf("unexpected cmd: %s %v", name, args)
			}
			switch args[0] {
			case "-getwebproxy":
				// 匹配本机端口 7890，应关闭
				return "Enabled: Yes\nServer: 127.0.0.1\nPort: 7890\n", nil
			case "-getsecurewebproxy":
				// 指向公司手工配置的代理端口 8080，不应误关
				return "Enabled: Yes\nServer: 10.0.0.5\nPort: 8080\n", nil
			case "-getsocksfirewallproxy":
				// 匹配本机端口 7890，应关闭
				return "Enabled: Yes\nServer: 127.0.0.1\nPort: 7890\n", nil
			case "-setwebproxystate", "-setsecurewebproxystate", "-setsocksfirewallproxystate":
				disabledFlags = append(disabledFlags, args[0]+"="+args[2])
				return "", nil
			default:
				return "", nil
			}
		},
	}

	changed, err := mgr.DisableServiceIfMatches("USB 10/100/1000 LAN", "127.0.0.1", 7890)
	if err != nil {
		t.Fatalf("DisableServiceIfMatches 失败: %v", err)
	}
	if !changed {
		t.Fatalf("期望 changed=true")
	}
	want := []string{"-setwebproxystate=off", "-setsocksfirewallproxystate=off"}
	if !reflect.DeepEqual(disabledFlags, want) {
		t.Fatalf("关闭项不匹配: got=%v, want=%v", disabledFlags, want)
	}
}

func TestDarwinManagerIsEnabledFallsBackToTargetServicesWhenExcluded(t *testing.T) {
	mgr := &darwinManager{
		run: func(name string, args ...string) (string, error) {
			if name == "scutil" {
				// 模拟当前主网卡（被排除的 USB 网卡）未开代理，因此 scutil 返回关闭
				return "<dictionary> {\n  HTTPEnable : 0\n  HTTPSEnable : 0\n}\n", nil
			}
			if name == "networksetup" && len(args) == 1 && args[0] == "-listallnetworkservices" {
				return "An asterisk (*) denotes that a network service is disabled.\nUSB 10/100/1000 LAN\nWi-Fi\n", nil
			}
			if name == "networksetup" && len(args) == 2 && args[1] == "Wi-Fi" && args[0] == "-getwebproxy" {
				return "Enabled: Yes\nServer: 127.0.0.1\nPort: 7890\n", nil
			}
			return "Enabled: No\nServer: \nPort: 0\n", nil
		},
	}
	mgr.SetExcludedServices([]string{"USB 10/100/1000 LAN"})

	on, err := mgr.IsEnabled("127.0.0.1", 7890)
	if err != nil {
		t.Fatalf("IsEnabled 失败: %v", err)
	}
	if !on {
		t.Fatalf("已排除主网卡但未排除网卡仍开启本机代理时，IsEnabled 应返回 true")
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
