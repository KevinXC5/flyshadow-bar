package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLosslessSavePreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// 模拟带有敏感账号字段与自定义未知字段的官方配置文件（均为测试假数据）
	original := `{
  "appAccountToken": "fake-token-keep-me",
  "subscribePassword": "fake-sub-pass-keep-me",
  "unknownNestedObj": {"a": 1, "b": ["x", "y"]},
  "proxyPort": 7890,
  "allowLanConnection": false,
  "proxyType": 3,
  "udpProxyType": 3,
  "udpAccelerationEnabled": true,
  "nativeUdp": false,
  "ipv6Enable": true,
  "useBuildInDns": true,
  "directConnPriority": false,
  "directConnPriorityTimeout": 200,
  "starNodeIdList": [101],
  "customRuleActiveLabels": ["__DEFAULT__", "ai"],
  "customRuleList": [
    {"id": 1, "enable": 1, "label": "", "domain": "example.com", "matching": 1, "proxyType": 2, "directConnPriority": 0},
    {"id": 2, "enable": 0, "label": "", "domain": "disabled.com", "matching": 1, "proxyType": 2, "directConnPriority": 0},
    {"id": 3, "enable": 1, "label": "ai", "domain": "openai.com", "matching": 1, "proxyType": 2, "directConnPriority": 0},
    {"id": 4, "enable": 1, "label": "inactive", "domain": "skip.com", "matching": 1, "proxyType": 2, "directConnPriority": 0}
  ],
  "subscribeInfo": {
    "user": {"email": "test@example.invalid"},
    "node": [
      {"id": 101, "name": "HK-01", "cnName": "香港 01", "host": "127.0.0.1", "port": 443, "password": "fake-node-pass-1", "enableQuic": 1, "nativeUdp": 1},
      {"id": 102, "name": "JP-02", "cnName": "日本 02", "host": "127.0.0.1", "port": 444, "password": "fake-node-pass-2", "enableQuic": 0, "nativeUdp": 1}
    ],
    "customRule": [
      {"domain": "google.com", "matching": 1, "proxyType": 2, "directConnPriority": 0}
    ]
  },
  "usedNodeInfo": {
    "id": 101, "name": "HK-01", "cnName": "香港 01", "host": "127.0.0.1", "port": 443, "password": "fake-node-pass-1", "enableQuic": 1, "nativeUdp": 1
  }
}`
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 验证规则过滤逻辑
	rules := cfg.ActiveRules()
	if len(rules) != 3 {
		t.Fatalf("expected 3 active rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].Domain != "example.com" || rules[1].Domain != "openai.com" || rules[2].Domain != "google.com" {
		t.Fatalf("unexpected active rule order/domains: %+v", rules)
	}

	// 验证节点查找与收藏切换
	node, err := cfg.FindNode("日本")
	if err != nil || node.ID != 102 {
		t.Fatalf("FindNode('日本') = (%+v, %v)", node, err)
	}
	nodeByID, err := cfg.FindNode("101")
	if err != nil || nodeByID.ID != 101 {
		t.Fatalf("FindNode('101') = (%+v, %v)", nodeByID, err)
	}

	// 修改部分配置并写回
	used := node.ToUsedNodeInfo()
	cfg.UsedNodeInfo = &used
	cfg.ProxyType = 2
	cfg.UDPAccelerationEnabled = false
	cfg.NativeUDP = true
	cfg.ToggleStar(102)

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 重新读取原始 JSON，检查未知字段和敏感字段毫发无损
	savedBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read saved: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(savedBytes, &raw); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if raw["appAccountToken"] != "fake-token-keep-me" {
		t.Fatalf("appAccountToken lost or modified: %v", raw["appAccountToken"])
	}
	if raw["subscribePassword"] != "fake-sub-pass-keep-me" {
		t.Fatalf("subscribePassword lost or modified: %v", raw["subscribePassword"])
	}
	if _, ok := raw["unknownNestedObj"]; !ok {
		t.Fatalf("unknownNestedObj was not preserved")
	}

	reloaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.ProxyType != 2 || reloaded.UDPAccelerationEnabled != false || reloaded.NativeUDP != true {
		t.Fatalf("reloaded fields mismatch: %+v", reloaded)
	}
	if reloaded.UsedNodeInfo == nil || reloaded.UsedNodeInfo.ID != 102 {
		t.Fatalf("reloaded UsedNodeInfo mismatch: %+v", reloaded.UsedNodeInfo)
	}
	if !reloaded.IsStarred(101) || !reloaded.IsStarred(102) {
		t.Fatalf("star list mismatch: %v", reloaded.StarNodeIDList)
	}

	// 验证自定义规则增删改与标签组切换
	added, err := reloaded.AddCustomRule(DomainRule{
		Domain:    "claude.ai",
		Matching:  MatchDomainSuffix,
		ProxyType: 2,
		Label:     "work",
	})
	if err != nil || added.ID <= 4 {
		t.Fatalf("AddCustomRule = (%+v, %v)", added, err)
	}
	if !reloaded.IsRuleLabelActive("work") {
		t.Fatalf("expected new label 'work' to be auto-activated")
	}
	enabled, err := reloaded.ToggleCustomRule(added.ID)
	if err != nil || enabled {
		t.Fatalf("ToggleCustomRule = (%v, %v)", enabled, err)
	}
	added.Domain = "anthropic.com"
	added.Enable = IntPtr(1)
	if _, err := reloaded.UpdateCustomRule(added); err != nil {
		t.Fatalf("UpdateCustomRule: %v", err)
	}
	if err := reloaded.Save(); err != nil {
		t.Fatalf("Save after rule update: %v", err)
	}
	reloaded2, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload2: %v", err)
	}
	if len(reloaded2.CustomRuleList) != 5 || reloaded2.CustomRuleList[0].Domain != "anthropic.com" {
		t.Fatalf("unexpected custom rules after save: %+v", reloaded2.CustomRuleList)
	}
	if err := reloaded2.DeleteCustomRule(added.ID); err != nil {
		t.Fatalf("DeleteCustomRule: %v", err)
	}
	if len(reloaded2.CustomRuleList) != 4 {
		t.Fatalf("expected 4 custom rules after delete, got %d", len(reloaded2.CustomRuleList))
	}
}
