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

func TestLoadInitializesDefaultConfigWhenMissingAndSavesSubscribePassword(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("expected zero-config Load to succeed, got: %v", err)
	}
	if cfg.ProxyPort != DefaultProxyPort || cfg.ProxyType != 3 || cfg.UUID == "" {
		t.Fatalf("unexpected default config: port=%d type=%d uuid=%q", cfg.ProxyPort, cfg.ProxyType, cfg.UUID)
	}

	cfg.SetSubscribePassword("mock-sub-secret-123")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.SubscribePassword != "mock-sub-secret-123" {
		t.Fatalf("subscribePassword mismatch: got %q", reloaded.SubscribePassword)
	}
	if reloaded.UUID != cfg.UUID {
		t.Fatalf("uuid mismatch: got %q, want %q", reloaded.UUID, cfg.UUID)
	}
}

func TestMigrateFromLegacyOnlineUpdateAndFreshInstall(t *testing.T) {
	// 场景 1：全新安装（不存在旧目录 ~/.FlyShadow）
	root := t.TempDir()
	freshTarget := filepath.Join(root, "fresh", AppDirName)
	missingLegacy := filepath.Join(root, "fresh", LegacyFlyShadowDirName)
	rep, err := MigrateFromLegacy(missingLegacy, freshTarget)
	if err != nil {
		t.Fatalf("全新安装迁移不应失败: %v", err)
	}
	if rep.CopiedConfig || rep.CopiedPrefs {
		t.Fatalf("全新安装不应复制任何文件: %+v", rep)
	}
	if st, err := os.Stat(freshTarget); err != nil || !st.IsDir() {
		t.Fatalf("应自动创建目标主目录: err=%v", err)
	}

	// 场景 2：旧版在线更新（~/.FlyShadow 中已有 config.json 与 prefs.json）
	legacyDir := filepath.Join(root, "upgrade", LegacyFlyShadowDirName)
	targetDir := filepath.Join(root, "upgrade", AppDirName)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wantCfg := `{"proxyPort":7899,"subscribePassword":"mock-migrate-pass","customField":"keep"}`
	wantPrefs := `{"themeMode":"dark","showTraySpeed":true}`
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(wantCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, PrefsFileName), []byte(wantPrefs), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err = MigrateFromLegacy(legacyDir, targetDir)
	if err != nil {
		t.Fatalf("升级迁移失败: %v", err)
	}
	if !rep.CopiedConfig || !rep.CopiedPrefs {
		t.Fatalf("应复制 config.json 与 prefs.json: %+v", rep)
	}
	gotCfg, err := os.ReadFile(filepath.Join(targetDir, "config.json"))
	if err != nil || string(gotCfg) != wantCfg {
		t.Fatalf("迁移后 config.json 不一致: err=%v got=%q", err, string(gotCfg))
	}
	gotPrefs, err := os.ReadFile(filepath.Join(targetDir, PrefsFileName))
	if err != nil || string(gotPrefs) != wantPrefs {
		t.Fatalf("迁移后 prefs.json 不一致: err=%v got=%q", err, string(gotPrefs))
	}
	// 旧目录文件必须原封不动保留，不破坏原版 FlyShadow
	if _, err := os.Stat(filepath.Join(legacyDir, "config.json")); err != nil {
		t.Fatalf("旧目录 config.json 不应被删除: %v", err)
	}

	// 场景 3：幂等性校验——新目录已有更新内容时，再次迁移绝不覆盖
	newerCfg := `{"proxyPort":8899}`
	if err := os.WriteFile(filepath.Join(targetDir, "config.json"), []byte(newerCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	rep2, err := MigrateFromLegacy(legacyDir, targetDir)
	if err != nil {
		t.Fatalf("二次迁移不应报错: %v", err)
	}
	if rep2.CopiedConfig || rep2.CopiedPrefs {
		t.Fatalf("新目录已存在文件时不应重复复制: %+v", rep2)
	}
	gotCfg2, _ := os.ReadFile(filepath.Join(targetDir, "config.json"))
	if string(gotCfg2) != newerCfg {
		t.Fatalf("二次迁移覆盖了新目录配置: got=%q want=%q", string(gotCfg2), newerCfg)
	}
}

func TestEnsureMigratedSkipsWhenCustomEnvSet(t *testing.T) {
	customDir := filepath.Join(t.TempDir(), "custom-dir")
	t.Setenv("FLYSHADOW_BAR_DIR", customDir)
	rep, err := EnsureMigrated()
	if err != nil {
		t.Fatalf("EnsureMigrated: %v", err)
	}
	if !rep.SkippedCustom || rep.CopiedConfig || rep.CopiedPrefs {
		t.Fatalf("设置自定义环境变量时应跳过迁移: %+v", rep)
	}
}

func TestUpdateAppPrefsPreservesOtherFieldsAndNormalizesServices(t *testing.T) {
	dir := t.TempDir()
	prefsPath := PrefsPathForConfig(filepath.Join(dir, "config.json"))
	if prefsPath != filepath.Join(dir, PrefsFileName) {
		t.Fatalf("PrefsPathForConfig 路径异常: got=%q", prefsPath)
	}

	if _, err := UpdateAppPrefs(prefsPath, func(p *AppPrefs) {
		p.ThemeMode = "dark"
		p.ShowTraySpeed = true
	}); err != nil {
		t.Fatalf("首次 UpdateAppPrefs 失败: %v", err)
	}

	updated, err := UpdateAppPrefs(prefsPath, func(p *AppPrefs) {
		p.SysProxyExcludedServices = []string{" USB 10/100/1000 LAN ", "", "USB 10/100/1000 LAN", "Thunderbolt Bridge"}
	})
	if err != nil {
		t.Fatalf("二次 UpdateAppPrefs 失败: %v", err)
	}
	if updated.ThemeMode != "dark" || !updated.ShowTraySpeed {
		t.Fatalf("更新排除网卡时不应丢失已有界面偏好: %+v", updated)
	}
	if len(updated.SysProxyExcludedServices) != 2 ||
		updated.SysProxyExcludedServices[0] != "USB 10/100/1000 LAN" ||
		updated.SysProxyExcludedServices[1] != "Thunderbolt Bridge" {
		t.Fatalf("排除网卡列表规范化异常: %+v", updated.SysProxyExcludedServices)
	}

	reloaded := LoadAppPrefs(prefsPath)
	if reloaded.ThemeMode != "dark" || !reloaded.ShowTraySpeed || len(reloaded.SysProxyExcludedServices) != 2 {
		t.Fatalf("重新读取 AppPrefs 不一致: %+v", reloaded)
	}
}
