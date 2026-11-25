package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultProxyPort          = 7890
	DefaultDirectConnTimeout  = 200
	DefaultRuleActiveSentinel = "__DEFAULT__"
)

// RuleMatching 定义与 FlyShadow 核心库完全一致的规则匹配类型枚举（0~12）。
const (
	MatchDomainExact    = 0  // 域名完全匹配
	MatchDomainSuffix   = 1  // 域名后缀
	MatchDomainKeyword  = 2  // 域名关键词
	MatchCIDR4          = 3  // IPv4 CIDR
	MatchCIDR6          = 4  // IPv6 CIDR
	MatchSrcCIDR4       = 5  // 源 IPv4 CIDR
	MatchGeoIP          = 6  // GeoIP 国家代码
	MatchDstPort        = 7  // 目标端口
	MatchSrcPort        = 8  // 源端口
	MatchProcessName    = 9  // 进程名
	MatchAll            = 10 // 匹配全部
	MatchClashClassical = 11 // Clash Classical
	MatchClashDomain    = 12 // Clash Domain
)

// RuleMatchingLabel 返回匹配类型的简体中文展示名。
func RuleMatchingLabel(m int) string {
	switch m {
	case MatchDomainExact:
		return "域名完全"
	case MatchDomainSuffix:
		return "域名后缀"
	case MatchDomainKeyword:
		return "域名关键词"
	case MatchCIDR4:
		return "IPv4 CIDR"
	case MatchCIDR6:
		return "IPv6 CIDR"
	case MatchSrcCIDR4:
		return "源 IPv4"
	case MatchGeoIP:
		return "GeoIP"
	case MatchDstPort:
		return "目标端口"
	case MatchSrcPort:
		return "源端口"
	case MatchProcessName:
		return "进程名"
	case MatchAll:
		return "全部匹配"
	case MatchClashClassical:
		return "Clash Classical"
	case MatchClashDomain:
		return "Clash Domain"
	default:
		return fmt.Sprintf("类型(%d)", m)
	}
}

// ParseRuleMatching 解析 CLI/API 传入的匹配类型名称或编号。
func ParseRuleMatching(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "exact", "domain", "full", "完全", "域名完全":
		return MatchDomainExact, nil
	case "1", "suffix", "domain-suffix", "后缀", "域名后缀", "":
		return MatchDomainSuffix, nil
	case "2", "keyword", "domain-keyword", "关键词", "域名关键词":
		return MatchDomainKeyword, nil
	case "3", "cidr", "cidr4", "ip-cidr", "ipv4":
		return MatchCIDR4, nil
	case "4", "cidr6", "ip-cidr6", "ipv6":
		return MatchCIDR6, nil
	case "5", "src-cidr4", "src-ip":
		return MatchSrcCIDR4, nil
	case "6", "geoip":
		return MatchGeoIP, nil
	case "7", "dstport", "dst-port", "port":
		return MatchDstPort, nil
	case "8", "srcport", "src-port":
		return MatchSrcPort, nil
	case "9", "process", "process-name", "进程", "进程名":
		return MatchProcessName, nil
	case "10", "all", "match":
		return MatchAll, nil
	case "11", "clash-classical":
		return MatchClashClassical, nil
	case "12", "clash-domain":
		return MatchClashDomain, nil
	default:
		return MatchDomainSuffix, fmt.Errorf("不支持的匹配类型 %q（支持: suffix, exact, keyword, cidr4, cidr6, process, dstport, geoip）", s)
	}
}

// RuleActionLabel 返回规则动作（0=直连, 1=拒绝, 2=代理）的中文名。
func RuleActionLabel(proxyType int) string {
	switch proxyType {
	case 0:
		return "直连"
	case 1:
		return "拒绝"
	case 2:
		return "代理"
	default:
		return "规则"
	}
}

// ParseRuleAction 解析规则动作字符串（direct / reject / proxy）。
func ParseRuleAction(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "direct", "直连":
		return 0, nil
	case "1", "reject", "block", "拒绝":
		return 1, nil
	case "2", "proxy", "global", "代理":
		return 2, nil
	default:
		return 2, fmt.Errorf("不支持的规则动作 %q（支持: proxy, direct, reject）", s)
	}
}

// Node 表示 FlyShadow 订阅中的一个节点。
// 注意：Password 属于敏感字段，任何日志与 CLI/UI 摘要输出都禁止打印它。
type Node struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	CNName      string   `json:"cnName"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Password    string   `json:"password"`
	EnableQuic  int      `json:"enableQuic"`
	NativeUDP   int      `json:"nativeUdp"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	TrafficRate float64  `json:"trafficRate,omitempty"`
}

// DisplayName 返回优先展示的节点名称（优先中文名）。
func (n Node) DisplayName() string {
	if s := strings.TrimSpace(n.CNName); s != "" {
		return s
	}
	if s := strings.TrimSpace(n.Name); s != "" {
		return s
	}
	return fmt.Sprintf("Node #%d", n.ID)
}

// SupportsQuic 返回节点本身是否支持 QUIC（UDP 加速）。
func (n Node) SupportsQuic() bool {
	return n.EnableQuic == 1
}

// SupportsNativeUDP 返回节点本身是否支持原生 UDP。
func (n Node) SupportsNativeUDP() bool {
	return n.NativeUDP == 1
}

// UsedNodeInfo 对应 config.json 里的 usedNodeInfo 结构。
type UsedNodeInfo struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	CNName      string `json:"cnName"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Password    string `json:"password"`
	EnableQuic  int    `json:"enableQuic"`
	NativeUDP   int    `json:"nativeUdp"`
	Description string `json:"description,omitempty"`
}

// ToUsedNodeInfo 将 Node 转换为写回 config.json 的 UsedNodeInfo。
func (n Node) ToUsedNodeInfo() UsedNodeInfo {
	return UsedNodeInfo{
		ID:          n.ID,
		Name:        n.Name,
		CNName:      n.CNName,
		Host:        n.Host,
		Port:        n.Port,
		Password:    n.Password,
		EnableQuic:  n.EnableQuic,
		NativeUDP:   n.NativeUDP,
		Description: n.Description,
	}
}

// ToNode 将 UsedNodeInfo 转为 Node。
func (u UsedNodeInfo) ToNode() Node {
	return Node{
		ID:          u.ID,
		Name:        u.Name,
		CNName:      u.CNName,
		Host:        u.Host,
		Port:        u.Port,
		Password:    u.Password,
		EnableQuic:  u.EnableQuic,
		NativeUDP:   u.NativeUDP,
		Description: u.Description,
	}
}

// FlexMillis 兼容官方 GsonUtil 的日期编解码：反序列化同时支持毫秒时间戳与日期字符串，
// 序列化统一写为毫秒整数，确保与官方 config.json 及云端 API 完全兼容。
type FlexMillis int64

func (m *FlexMillis) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		*m = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*m = FlexMillis(n)
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	str = strings.TrimSpace(str)
	if str == "" {
		*m = 0
		return nil
	}
	if n, err := strconv.ParseInt(str, 10, 64); err == nil {
		*m = FlexMillis(n)
		return nil
	}
	// 官方服务端时间默认 GMT+8，同时兼容常见 ISO8601 / RFC3339 格式
	cst := time.FixedZone("CST", 8*3600)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, str, cst); err == nil {
			*m = FlexMillis(t.UnixMilli())
			return nil
		}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, str); err == nil {
			*m = FlexMillis(t.UnixMilli())
			return nil
		}
	}
	return fmt.Errorf("无法解析时间戳: %q", str)
}

func (m FlexMillis) MarshalJSON() ([]byte, error) {
	return strconv.AppendInt(nil, int64(m), 10), nil
}

// DomainRule 表示一条域名/网络分流规则（来自 subscribeInfo.customRule 或 customRuleList）。
type DomainRule struct {
	ID                 int        `json:"id,omitempty"`
	Enable             *int       `json:"enable,omitempty"`
	Label              string     `json:"label"`
	Domain             string     `json:"domain"`
	Matching           int        `json:"matching"`
	ProxyType          int        `json:"proxyType"`
	DirectConnPriority int        `json:"directConnPriority"`
	CreateTime         FlexMillis `json:"createTime,omitempty"`
	Sort               int        `json:"sort"`
}

// IsEnabled 返回该自定义规则是否启用（未显式设置时默认启用）。
func (r DomainRule) IsEnabled() bool {
	return r.Enable == nil || *r.Enable == 1
}

// IntPtr 构造 int 指针。
func IntPtr(v int) *int { return &v }

// UserTrafficInfo 保存订阅用户的非敏感流量、VIP 等级与到期统计。
type UserTrafficInfo struct {
	TrafficSize     int64      `json:"trafficSize"`
	TrafficUpload   int64      `json:"trafficUpload"`
	TrafficDownload int64      `json:"trafficDownload"`
	Level           int        `json:"level,omitempty"`
	ExpireTime      FlexMillis `json:"expireTime"` // 毫秒时间戳
}

// SubscribeInfo 只解析我们关心的节点、内置规则与流量统计，其余字段由 raw map 无损保留。
type SubscribeInfo struct {
	Nodes       []Node           `json:"node"`
	CustomRules []DomainRule     `json:"customRule"`
	User        *UserTrafficInfo `json:"user,omitempty"`
}

// Config 表示解析后的 FlyShadow 配置，同时保留原始 JSON 字典以实现无损写回。
type Config struct {
	Path string

	SubscribeInfo             SubscribeInfo
	SubscribePassword         string // 仅用于调用官方订阅与规则同步 API，绝不暴露至快照或日志
	LastUpdateTime            FlexMillis
	CustomRuleList            []DomainRule
	CustomRuleActiveLabels    []string
	UsedNodeInfo              *UsedNodeInfo
	ProxyPort                 int
	AllowLanConnection        bool
	ProxyUsername             string
	ProxyPassword             string
	ProxyType                 int
	UDPProxyType              int
	UDPAccelerationEnabled    bool
	NativeUDP                 bool
	IPv6Enable                bool
	UseBuildInDNS             bool
	DirectConnPriority        bool
	DirectConnPriorityTimeout int64
	AutoSetSysProxyOnStartUp  bool
	UUID                      string
	StarNodeIDList            []int

	subscribeDirty         bool
	subscribePasswordDirty bool
	uuidDirty              bool
	raw                    map[string]json.RawMessage
}

// AppPrefs 保存 flyshadow-bar 自身的轻量界面偏好，避免污染官方 config.json。
type AppPrefs struct {
	ThemeMode     string `json:"themeMode"`     // system | light | dark
	ShowTraySpeed bool   `json:"showTraySpeed"` // macOS 菜单栏是否显示实时速率
	UpdateRepo    string `json:"updateRepo,omitempty"`
}

const (
	// AppDirName 是 FlyShadow Bar 独立的程序主目录名（位于用户主目录下）。
	AppDirName = ".flyshadowbar"
	// LegacyFlyShadowDirName 是原版 FlyShadow 客户端的数据目录名，仅用于首次迁移与只读查找官方核心库。
	LegacyFlyShadowDirName = ".FlyShadow"
	// PrefsFileName 是客户端轻量界面偏好文件名。
	PrefsFileName = "flyshadow-bar.prefs.json"
	// LogFileName 是程序滚动日志文件名。
	LogFileName = "flyshadow-bar.log"
)

// MigrationReport 记录从旧目录（~/.FlyShadow）向独立主目录（~/.flyshadowbar）的兼容迁移结果。
type MigrationReport struct {
	LegacyDir     string
	TargetDir     string
	CopiedConfig  bool
	CopiedPrefs   bool
	SkippedCustom bool
}

// HasCustomDirEnv 判断是否通过环境变量显式指定了自定义目录或配置路径。
func HasCustomDirEnv() bool {
	return strings.TrimSpace(os.Getenv("FLYSHADOW_BAR_DIR")) != "" ||
		strings.TrimSpace(os.Getenv("FLYSHADOW_CONFIG_DIR")) != "" ||
		strings.TrimSpace(os.Getenv("FLYSHADOW_CONFIG_PATH")) != ""
}

// DefaultDir 返回 FlyShadow Bar 独立程序主目录（默认 ~/.flyshadowbar）。
func DefaultDir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("FLYSHADOW_BAR_DIR")); custom != "" {
		return custom, nil
	}
	if custom := strings.TrimSpace(os.Getenv("FLYSHADOW_CONFIG_DIR")); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, AppDirName), nil
}

// LegacyDir 返回原版 FlyShadow 目录（~/.FlyShadow），仅作只读查找与兼容迁移来源。
func LegacyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, LegacyFlyShadowDirName), nil
}

// CacheDir 返回 FlyShadow Bar 自身的解压缓存目录（~/.flyshadowbar/cache）。
func CacheDir() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache"), nil
}

// DefaultLogPath 返回程序日志文件路径（默认 ~/.flyshadowbar/logs/flyshadow-bar.log）。
func DefaultLogPath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("FLYSHADOW_BAR_LOG_FILE")); custom != "" {
		return custom, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", LogFileName), nil
}

// DefaultConfigPath 返回默认配置文件路径 ~/.flyshadowbar/config.json。
func DefaultConfigPath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("FLYSHADOW_CONFIG_PATH")); custom != "" {
		return custom, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DefaultPrefsPath 返回 flyshadow-bar 自身偏好文件路径 ~/.flyshadowbar/flyshadow-bar.prefs.json。
func DefaultPrefsPath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, PrefsFileName), nil
}

// EnsureMigrated 确保独立主目录 ~/.flyshadowbar 已初始化；
// 若用户是从旧版本在线更新或曾使用过原版 FlyShadow，自动将 ~/.FlyShadow 中的配置与界面偏好
// 无损复制到 ~/.flyshadowbar（绝不删除、不修改旧目录，且不覆盖新目录中已存在的文件）。
// 当检测到显式自定义目录/配置环境变量时自动跳过，避免干扰隔离测试环境。
func EnsureMigrated() (MigrationReport, error) {
	if HasCustomDirEnv() {
		dir, _ := DefaultDir()
		return MigrationReport{TargetDir: dir, SkippedCustom: true}, nil
	}
	targetDir, err := DefaultDir()
	if err != nil {
		return MigrationReport{}, err
	}
	legacyDir, err := LegacyDir()
	if err != nil {
		return MigrationReport{TargetDir: targetDir}, nil
	}
	return MigrateFromLegacy(legacyDir, targetDir)
}

// MigrateFromLegacy 将 legacyDir 中属于本客户端的配置与偏好安全复制到 targetDir。
func MigrateFromLegacy(legacyDir, targetDir string) (MigrationReport, error) {
	report := MigrationReport{
		LegacyDir: legacyDir,
		TargetDir: targetDir,
	}
	if strings.TrimSpace(targetDir) == "" {
		return report, errors.New("目标主目录不能为空")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return report, fmt.Errorf("创建程序主目录失败 (%s): %w", targetDir, err)
	}
	if strings.TrimSpace(legacyDir) == "" || filepath.Clean(legacyDir) == filepath.Clean(targetDir) {
		return report, nil
	}
	if st, err := os.Stat(legacyDir); err != nil || !st.IsDir() {
		return report, nil
	}

	copiedCfg, err := copyFileIfMissing(
		filepath.Join(legacyDir, "config.json"),
		filepath.Join(targetDir, "config.json"),
		0o600,
	)
	if err != nil {
		return report, fmt.Errorf("迁移配置文件失败: %w", err)
	}
	report.CopiedConfig = copiedCfg

	copiedPrefs, err := copyFileIfMissing(
		filepath.Join(legacyDir, PrefsFileName),
		filepath.Join(targetDir, PrefsFileName),
		0o600,
	)
	if err != nil {
		return report, fmt.Errorf("迁移界面偏好失败: %w", err)
	}
	report.CopiedPrefs = copiedPrefs

	return report, nil
}

// copyFileIfMissing 仅在 dst 不存在且 src 为非空普通文件时执行原子复制。
func copyFileIfMissing(src, dst string, perm os.FileMode) (bool, error) {
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	st, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if st.IsDir() || st.Size() == 0 {
		return false, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	if err := atomicWriteFile(dst, data, perm); err != nil {
		return false, err
	}
	return true, nil
}

// Load 读取并解析 FlyShadow 的 config.json，同时保留全部未知字段的原始 JSON。
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 全新环境无 config.json 时自动初始化默认配置，用户可直接在客户端内填订阅密码完成初始化
			return &Config{
				Path:                      path,
				ProxyPort:                 DefaultProxyPort,
				ProxyType:                 3, // 默认 Rule 模式
				UDPProxyType:              3,
				UseBuildInDNS:             true,
				DirectConnPriorityTimeout: DefaultDirectConnTimeout,
				UUID:                      newRandomUUID(),
				uuidDirty:                 true,
				raw:                       make(map[string]json.RawMessage),
			}, nil
		}
		return nil, fmt.Errorf("读取配置文件失败 (%s): %w", path, err)
	}

	raw := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析配置文件 JSON 失败: %w", err)
	}

	cfg := &Config{
		Path:                      path,
		ProxyPort:                 DefaultProxyPort,
		ProxyType:                 3, // 默认 Rule 模式
		UDPProxyType:              3,
		UseBuildInDNS:             true,
		DirectConnPriorityTimeout: DefaultDirectConnTimeout,
		raw:                       raw,
	}

	unmarshalField(raw, "subscribeInfo", &cfg.SubscribeInfo)
	unmarshalField(raw, "subscribePassword", &cfg.SubscribePassword)
	unmarshalField(raw, "lastUpdateTime", &cfg.LastUpdateTime)
	unmarshalField(raw, "customRuleList", &cfg.CustomRuleList)
	unmarshalField(raw, "customRuleActiveLabels", &cfg.CustomRuleActiveLabels)
	unmarshalField(raw, "usedNodeInfo", &cfg.UsedNodeInfo)
	unmarshalField(raw, "proxyPort", &cfg.ProxyPort)
	unmarshalField(raw, "allowLanConnection", &cfg.AllowLanConnection)
	unmarshalField(raw, "proxyUsername", &cfg.ProxyUsername)
	unmarshalField(raw, "proxyPassword", &cfg.ProxyPassword)
	unmarshalField(raw, "proxyType", &cfg.ProxyType)
	unmarshalField(raw, "udpProxyType", &cfg.UDPProxyType)
	unmarshalField(raw, "udpAccelerationEnabled", &cfg.UDPAccelerationEnabled)
	unmarshalField(raw, "nativeUdp", &cfg.NativeUDP)
	unmarshalField(raw, "ipv6Enable", &cfg.IPv6Enable)
	unmarshalField(raw, "useBuildInDns", &cfg.UseBuildInDNS)
	unmarshalField(raw, "directConnPriority", &cfg.DirectConnPriority)
	unmarshalField(raw, "directConnPriorityTimeout", &cfg.DirectConnPriorityTimeout)
	unmarshalField(raw, "autoSetSysProxyOnStartUp", &cfg.AutoSetSysProxyOnStartUp)
	unmarshalField(raw, "uuid", &cfg.UUID)
	unmarshalField(raw, "starNodeIdList", &cfg.StarNodeIDList)

	if cfg.ProxyPort <= 0 || cfg.ProxyPort > 65535 {
		cfg.ProxyPort = DefaultProxyPort
	}
	if cfg.DirectConnPriorityTimeout <= 0 {
		cfg.DirectConnPriorityTimeout = DefaultDirectConnTimeout
	}

	return cfg, nil
}

// Save 将当前受控字段无损合并回 raw map 并以原子方式写回 config.json。
// 原文件中的账号 token、订阅信息等其他字段会被完整保留，不做任何改动。
func (c *Config) Save() error {
	if c == nil {
		return errors.New("配置为空")
	}
	if c.Path == "" {
		return errors.New("配置文件路径为空")
	}
	if c.raw == nil {
		c.raw = make(map[string]json.RawMessage)
	}

	if c.subscribeDirty {
		if err := setRawField(c.raw, "subscribeInfo", c.SubscribeInfo); err != nil {
			return err
		}
		if c.LastUpdateTime > 0 {
			if err := setRawField(c.raw, "lastUpdateTime", c.LastUpdateTime); err != nil {
				return err
			}
		}
	}
	if c.subscribePasswordDirty {
		if err := setRawField(c.raw, "subscribePassword", c.SubscribePassword); err != nil {
			return err
		}
	}
	if c.uuidDirty && c.UUID != "" {
		if err := setRawField(c.raw, "uuid", c.UUID); err != nil {
			return err
		}
	}
	if c.UsedNodeInfo != nil {
		if err := setRawField(c.raw, "usedNodeInfo", c.UsedNodeInfo); err != nil {
			return err
		}
	}
	if err := setRawField(c.raw, "proxyPort", c.ProxyPort); err != nil {
		return err
	}
	if err := setRawField(c.raw, "allowLanConnection", c.AllowLanConnection); err != nil {
		return err
	}
	if err := setRawField(c.raw, "proxyType", c.ProxyType); err != nil {
		return err
	}
	if err := setRawField(c.raw, "udpProxyType", c.UDPProxyType); err != nil {
		return err
	}
	if err := setRawField(c.raw, "udpAccelerationEnabled", c.UDPAccelerationEnabled); err != nil {
		return err
	}
	if err := setRawField(c.raw, "nativeUdp", c.NativeUDP); err != nil {
		return err
	}
	if err := setRawField(c.raw, "ipv6Enable", c.IPv6Enable); err != nil {
		return err
	}
	if err := setRawField(c.raw, "useBuildInDns", c.UseBuildInDNS); err != nil {
		return err
	}
	if err := setRawField(c.raw, "directConnPriority", c.DirectConnPriority); err != nil {
		return err
	}
	if err := setRawField(c.raw, "directConnPriorityTimeout", c.DirectConnPriorityTimeout); err != nil {
		return err
	}
	if err := setRawField(c.raw, "autoSetSysProxyOnStartUp", c.AutoSetSysProxyOnStartUp); err != nil {
		return err
	}
	starList := c.StarNodeIDList
	if starList == nil {
		starList = []int{}
	}
	if err := setRawField(c.raw, "starNodeIdList", starList); err != nil {
		return err
	}

	ruleList := c.CustomRuleList
	if ruleList == nil {
		ruleList = []DomainRule{}
	}
	if err := setRawField(c.raw, "customRuleList", ruleList); err != nil {
		return err
	}
	if c.CustomRuleActiveLabels != nil {
		if err := setRawField(c.raw, "customRuleActiveLabels", c.CustomRuleActiveLabels); err != nil {
			return err
		}
	}

	encoded, err := json.MarshalIndent(c.raw, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	return atomicWriteFile(c.Path, encoded, 0o600)
}

// AddCustomRule 新增一条自定义规则（插入列表头部，自动分配递增 ID 并激活其所属标签组）。
func (c *Config) AddCustomRule(rule DomainRule) (DomainRule, error) {
	if c == nil {
		return DomainRule{}, errors.New("配置为空")
	}
	rule.Domain = strings.TrimSpace(rule.Domain)
	rule.Label = strings.TrimSpace(rule.Label)
	if rule.Domain == "" {
		return DomainRule{}, errors.New("规则值（域名/IP/关键词）不能为空")
	}
	if rule.Enable == nil {
		rule.Enable = IntPtr(1)
	}
	if rule.CreateTime == 0 {
		rule.CreateTime = FlexMillis(time.Now().UnixMilli())
	}
	if rule.ID <= 0 {
		maxID := 0
		for _, r := range c.CustomRuleList {
			if r.ID > maxID {
				maxID = r.ID
			}
		}
		rule.ID = maxID + 1
	}

	// 新规则默认插入列表最前，优先级最高
	c.CustomRuleList = append([]DomainRule{rule}, c.CustomRuleList...)
	c.ensureRuleLabelActive(rule.Label)
	return rule, nil
}

// UpdateCustomRule 按 ID 更新已有自定义规则。
func (c *Config) UpdateCustomRule(rule DomainRule) (DomainRule, error) {
	if c == nil {
		return DomainRule{}, errors.New("配置为空")
	}
	rule.Domain = strings.TrimSpace(rule.Domain)
	rule.Label = strings.TrimSpace(rule.Label)
	if rule.Domain == "" {
		return DomainRule{}, errors.New("规则值不能为空")
	}
	for i := range c.CustomRuleList {
		if c.CustomRuleList[i].ID == rule.ID {
			if rule.Enable == nil {
				rule.Enable = c.CustomRuleList[i].Enable
				if rule.Enable == nil {
					rule.Enable = IntPtr(1)
				}
			}
			if rule.CreateTime == 0 {
				rule.CreateTime = c.CustomRuleList[i].CreateTime
			}
			c.CustomRuleList[i] = rule
			c.ensureRuleLabelActive(rule.Label)
			return rule, nil
		}
	}
	return DomainRule{}, fmt.Errorf("未找到 ID 为 %d 的自定义规则", rule.ID)
}

// DeleteCustomRule 按 ID 删除自定义规则。
func (c *Config) DeleteCustomRule(id int) error {
	if c == nil {
		return errors.New("配置为空")
	}
	for i := range c.CustomRuleList {
		if c.CustomRuleList[i].ID == id {
			c.CustomRuleList = append(c.CustomRuleList[:i], c.CustomRuleList[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("未找到 ID 为 %d 的自定义规则", id)
}

// ToggleCustomRule 切换指定自定义规则的启用状态，返回切换后的启用结果。
func (c *Config) ToggleCustomRule(id int) (bool, error) {
	if c == nil {
		return false, errors.New("配置为空")
	}
	for i := range c.CustomRuleList {
		if c.CustomRuleList[i].ID == id {
			next := !c.CustomRuleList[i].IsEnabled()
			v := 0
			if next {
				v = 1
			}
			c.CustomRuleList[i].Enable = &v
			return next, nil
		}
	}
	return false, fmt.Errorf("未找到 ID 为 %d 的自定义规则", id)
}

// SetSubscribePassword 更新内存中的订阅密码并标记待写回（仅在云端校验通过后由引擎调用）。
func (c *Config) SetSubscribePassword(password string) {
	if c == nil {
		return
	}
	c.SubscribePassword = strings.TrimSpace(password)
	c.subscribePasswordDirty = true
}

func newRandomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fsbar-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// ApplySyncedData 将云端拉取的最新订阅信息（节点、流量、到期时间、内置规则）与自定义规则合并到配置中，
// 同时按节点 ID 同步刷新当前选中节点参数，保留用户收藏、代理偏好及其他未建模字段。
func (c *Config) ApplySyncedData(sub SubscribeInfo, customRules []DomainRule, now time.Time) {
	if c == nil {
		return
	}
	c.SubscribeInfo = sub
	c.subscribeDirty = true
	if !now.IsZero() {
		c.LastUpdateTime = FlexMillis(now.UnixMilli())
	}
	if customRules != nil {
		c.CustomRuleList = append([]DomainRule(nil), customRules...)
	}

	// 若当前选中节点在最新订阅列表中存在，刷新其最新连接参数；若原节点已下线且新列表非空则切到首个节点
	if len(sub.Nodes) > 0 {
		if c.UsedNodeInfo != nil {
			matched := false
			for _, n := range sub.Nodes {
				if n.ID == c.UsedNodeInfo.ID {
					u := n.ToUsedNodeInfo()
					c.UsedNodeInfo = &u
					matched = true
					break
				}
			}
			if !matched {
				u := sub.Nodes[0].ToUsedNodeInfo()
				c.UsedNodeInfo = &u
			}
		} else {
			u := sub.Nodes[0].ToUsedNodeInfo()
			c.UsedNodeInfo = &u
		}
	}
}

// ReplaceCustomRules 使用云端最新自定义规则列表替换本地缓存，并确保新出现规则标签处于激活状态。
func (c *Config) ReplaceCustomRules(rules []DomainRule, activeLabel string) {
	if c == nil {
		return
	}
	if rules == nil {
		c.CustomRuleList = []DomainRule{}
	} else {
		c.CustomRuleList = append([]DomainRule(nil), rules...)
	}
	if activeLabel != "" {
		c.ensureRuleLabelActive(activeLabel)
	}
}

// FindCustomRule 按 ID 查找自定义规则副本。
func (c *Config) FindCustomRule(id int) (DomainRule, bool) {
	if c == nil {
		return DomainRule{}, false
	}
	for _, r := range c.CustomRuleList {
		if r.ID == id {
			return r, true
		}
	}
	return DomainRule{}, false
}

// RuleLabels 返回所有出现过的规则标签列表（始终以 "__DEFAULT__" 为首）。
func (c *Config) RuleLabels() []string {
	seen := map[string]struct{}{DefaultRuleActiveSentinel: {}}
	var extras []string
	for _, r := range c.CustomRuleList {
		lbl := strings.TrimSpace(r.Label)
		if lbl == "" {
			continue
		}
		if _, ok := seen[lbl]; !ok {
			seen[lbl] = struct{}{}
			extras = append(extras, lbl)
		}
	}
	for _, lbl := range c.CustomRuleActiveLabels {
		lbl = strings.TrimSpace(lbl)
		if lbl == "" || lbl == DefaultRuleActiveSentinel {
			continue
		}
		if _, ok := seen[lbl]; !ok {
			seen[lbl] = struct{}{}
			extras = append(extras, lbl)
		}
	}
	sort.Strings(extras)
	return append([]string{DefaultRuleActiveSentinel}, extras...)
}

// IsRuleLabelActive 判断某个标签组是否处于激活状态。
func (c *Config) IsRuleLabelActive(label string) bool {
	if len(c.CustomRuleActiveLabels) == 0 {
		return true
	}
	key := strings.TrimSpace(label)
	if key == "" {
		key = DefaultRuleActiveSentinel
	}
	for _, l := range c.CustomRuleActiveLabels {
		if l == key {
			return true
		}
	}
	return false
}

// SetRuleLabelActive 设置某个标签组的激活状态。
func (c *Config) SetRuleLabelActive(label string, active bool) {
	key := strings.TrimSpace(label)
	if key == "" {
		key = DefaultRuleActiveSentinel
	}
	if len(c.CustomRuleActiveLabels) == 0 && !active {
		// 原本为空表示全激活，此时先展开所有标签再移除目标标签
		c.CustomRuleActiveLabels = c.RuleLabels()
	}
	idx := -1
	for i, l := range c.CustomRuleActiveLabels {
		if l == key {
			idx = i
			break
		}
	}
	if active {
		if idx < 0 {
			c.CustomRuleActiveLabels = append(c.CustomRuleActiveLabels, key)
		}
		return
	}
	if idx >= 0 {
		c.CustomRuleActiveLabels = append(c.CustomRuleActiveLabels[:idx], c.CustomRuleActiveLabels[idx+1:]...)
	}
}

func (c *Config) ensureRuleLabelActive(label string) {
	if len(c.CustomRuleActiveLabels) == 0 {
		return
	}
	c.SetRuleLabelActive(label, true)
}

// Nodes 返回可用节点列表；若订阅列表为空但存在 usedNodeInfo，则返回单节点列表兜底。
func (c *Config) Nodes() []Node {
	if c == nil {
		return nil
	}
	if len(c.SubscribeInfo.Nodes) > 0 {
		out := make([]Node, len(c.SubscribeInfo.Nodes))
		copy(out, c.SubscribeInfo.Nodes)
		return out
	}
	if c.UsedNodeInfo != nil && c.UsedNodeInfo.Host != "" {
		return []Node{c.UsedNodeInfo.ToNode()}
	}
	return nil
}

// CurrentNode 返回当前选中的节点；若未命中订阅列表则回退到 UsedNodeInfo 或首个节点。
func (c *Config) CurrentNode() *Node {
	if c == nil {
		return nil
	}
	if c.UsedNodeInfo != nil {
		for _, n := range c.SubscribeInfo.Nodes {
			if n.ID == c.UsedNodeInfo.ID {
				cp := n
				return &cp
			}
		}
		if c.UsedNodeInfo.Host != "" {
			n := c.UsedNodeInfo.ToNode()
			return &n
		}
	}
	if len(c.SubscribeInfo.Nodes) > 0 {
		cp := c.SubscribeInfo.Nodes[0]
		return &cp
	}
	return nil
}

// FindNode 按 ID、精确名称或关键词（忽略大小写）匹配节点。
func (c *Config) FindNode(query string) (*Node, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, errors.New("节点关键词或 ID 不能为空")
	}
	nodes := c.Nodes()
	if len(nodes) == 0 {
		return nil, errors.New("配置中没有可用节点")
	}

	if id, err := strconv.Atoi(q); err == nil {
		for _, n := range nodes {
			if n.ID == id {
				cp := n
				return &cp, nil
			}
		}
	}

	ql := strings.ToLower(q)
	for _, n := range nodes {
		if strings.ToLower(strings.TrimSpace(n.CNName)) == ql ||
			strings.ToLower(strings.TrimSpace(n.Name)) == ql {
			cp := n
			return &cp, nil
		}
	}

	var matched []Node
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.CNName), ql) ||
			strings.Contains(strings.ToLower(n.Name), ql) ||
			strings.Contains(strings.ToLower(n.Description), ql) {
			matched = append(matched, n)
		}
	}
	if len(matched) == 1 {
		cp := matched[0]
		return &cp, nil
	}
	if len(matched) > 1 {
		names := make([]string, 0, min(5, len(matched)))
		for i := 0; i < len(matched) && i < 5; i++ {
			names = append(names, fmt.Sprintf("%s(#%d)", matched[i].DisplayName(), matched[i].ID))
		}
		return nil, fmt.Errorf("匹配到多个节点 (%d 个): %s，请提供更精确的名称或节点 ID", len(matched), strings.Join(names, ", "))
	}

	return nil, fmt.Errorf("未找到匹配 %q 的节点", q)
}

// IsStarred 判断节点是否被收藏。
func (c *Config) IsStarred(nodeID int) bool {
	if c == nil {
		return false
	}
	for _, id := range c.StarNodeIDList {
		if id == nodeID {
			return true
		}
	}
	return false
}

// ToggleStar 切换节点收藏状态，返回切换后的收藏结果。
func (c *Config) ToggleStar(nodeID int) bool {
	if c == nil {
		return false
	}
	for i, id := range c.StarNodeIDList {
		if id == nodeID {
			c.StarNodeIDList = append(c.StarNodeIDList[:i], c.StarNodeIDList[i+1:]...)
			return false
		}
	}
	c.StarNodeIDList = append(c.StarNodeIDList, nodeID)
	return true
}

// ActiveRules 按官方 FlyShadow 客户端逻辑生成需加载到核心的域名规则列表：
// 先加载已启用的用户自定义规则（customRuleList），再加载订阅下发规则（subscribeInfo.customRule）。
// ActiveRuleSource 标识一条生效规则的来源。
type ActiveRuleSource string

const (
	RuleSourceCustom    ActiveRuleSource = "custom"    // 用户自定义规则（优先级高于订阅内置规则）
	RuleSourceSubscribe ActiveRuleSource = "subscribe" // 订阅下发内置规则
)

// ActiveRuleEntry 表示按核心库加载顺序生效的单条分流规则及其来源。
type ActiveRuleEntry struct {
	Order  int              `json:"order"`
	Source ActiveRuleSource `json:"source"`
	Rule   DomainRule       `json:"rule"`
}

// ActiveRuleEntries 返回按核心库加载顺序排列的全部生效规则（含自定义与订阅内置来源标识）。
func (c *Config) ActiveRuleEntries() []ActiveRuleEntry {
	if c == nil {
		return nil
	}
	activeLabels := make(map[string]struct{}, len(c.CustomRuleActiveLabels))
	for _, l := range c.CustomRuleActiveLabels {
		activeLabels[l] = struct{}{}
	}
	hasLabelFilter := len(activeLabels) > 0
	_, defaultActive := activeLabels[DefaultRuleActiveSentinel]

	out := make([]ActiveRuleEntry, 0, len(c.CustomRuleList)+len(c.SubscribeInfo.CustomRules))
	for _, r := range c.CustomRuleList {
		if strings.TrimSpace(r.Domain) == "" {
			continue
		}
		if r.Enable != nil && *r.Enable != 1 {
			continue
		}
		if hasLabelFilter {
			lbl := strings.TrimSpace(r.Label)
			if lbl == "" {
				if !defaultActive {
					continue
				}
			} else if _, ok := activeLabels[lbl]; !ok {
				continue
			}
		}
		out = append(out, ActiveRuleEntry{
			Order:  len(out) + 1,
			Source: RuleSourceCustom,
			Rule:   r,
		})
	}
	for _, r := range c.SubscribeInfo.CustomRules {
		if strings.TrimSpace(r.Domain) == "" {
			continue
		}
		out = append(out, ActiveRuleEntry{
			Order:  len(out) + 1,
			Source: RuleSourceSubscribe,
			Rule:   r,
		})
	}
	return out
}

func (c *Config) ActiveRules() []DomainRule {
	entries := c.ActiveRuleEntries()
	if len(entries) == 0 {
		return nil
	}
	out := make([]DomainRule, len(entries))
	for i, e := range entries {
		out[i] = e.Rule
	}
	return out
}

// LoadAppPrefs 读取 flyshadow-bar 自身偏好文件，不存在时返回默认值。
func LoadAppPrefs(path string) AppPrefs {
	prefs := AppPrefs{
		ThemeMode:     "system",
		ShowTraySpeed: false,
	}
	if path == "" {
		var err error
		path, err = DefaultPrefsPath()
		if err != nil {
			return prefs
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return prefs
	}
	_ = json.Unmarshal(data, &prefs)
	switch prefs.ThemeMode {
	case "light", "dark", "system":
	default:
		prefs.ThemeMode = "system"
	}
	return prefs
}

// SaveAppPrefs 原子保存 flyshadow-bar 自身偏好文件。
func SaveAppPrefs(path string, prefs AppPrefs) error {
	if path == "" {
		var err error
		path, err = DefaultPrefsPath()
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0o600)
}

func unmarshalField(raw map[string]json.RawMessage, key string, dst any) {
	v, ok := raw[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return
	}
	_ = json.Unmarshal(v, dst)
}

func setRawField(raw map[string]json.RawMessage, key string, val any) error {
	b, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("序列化字段 %s 失败: %w", key, err)
	}
	raw[key] = json.RawMessage(b)
	return nil
}

// atomicWriteFile 先写入同目录临时文件并落盘，再重命名覆盖目标文件。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败 (%s): %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-flyshadow-bar-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("落盘同步失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("原子替换文件失败: %w", err)
	}
	cleanup = false
	return nil
}
