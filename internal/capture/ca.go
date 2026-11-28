package capture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	caCommonName     = "FlyShadow Bar Local MITM CA"
	caOrganization   = "FlyShadow Bar Local"
	maxCachedLeaves  = 256
	caValidityYears  = 10
	leafValidityDays = 365
)

// EnvSnippet 表示供自带证书库的 CLI / 开发工具一键复制的环境变量或配置命令。
type EnvSnippet struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CertStatus 描述本机专属 MITM 根证书的生成与操作系统信任状态。
type CertStatus struct {
	Generated         bool         `json:"generated"`
	CertPath          string       `json:"certPath"`
	KeyPath           string       `json:"keyPath"`
	CommonName        string       `json:"commonName"`
	FingerprintSHA256 string       `json:"fingerprintSha256,omitempty"`
	FingerprintSHA1   string       `json:"fingerprintSha1,omitempty"`
	NotAfter          time.Time    `json:"notAfter,omitempty"`
	Installed         bool         `json:"installed"`
	Trusted           bool         `json:"trusted"`
	StatusDetail      string       `json:"statusDetail"`
	InstallPromptHint string       `json:"installPromptHint"`
	EnvSnippets       []EnvSnippet `json:"envSnippets,omitempty"`
}

// CAManager 负责管理本机专属自签名 Root CA 及按域名动态签发的内存叶证书。
type CAManager struct {
	mu       sync.RWMutex
	dir      string
	certPath string
	keyPath  string

	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caDER    []byte
	sha256FP string
	sha1FP   string

	leafMu    sync.RWMutex
	leafCache map[string]*tls.Certificate
}

// NewCAManager 创建指定目录的根证书管理器（默认在 ~/.flyshadowbar/mitm 下隔离存放）。
func NewCAManager(dir string) *CAManager {
	return &CAManager{
		dir:       dir,
		certPath:  filepath.Join(dir, "flyshadow-bar-ca.crt"),
		keyPath:   filepath.Join(dir, "flyshadow-bar-ca.key"),
		leafCache: make(map[string]*tls.Certificate),
	}
}

// Dir 返回证书存储目录。
func (m *CAManager) Dir() string {
	return m.dir
}

// CertPath 返回 PEM 格式 Root CA 证书路径。
func (m *CAManager) CertPath() string {
	return m.certPath
}

// EnsureCA 加载已有本机 Root CA，若不存在或已失效则自动在本机随机生成全新密钥对与根证书。
func (m *CAManager) EnsureCA() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.caCert != nil && m.caKey != nil {
		return nil
	}
	if err := m.loadExistingLocked(); err == nil {
		return nil
	}
	return m.generateNewLocked()
}

// LoadExisting 仅尝试加载已有 CA（不自动创建文件）。
func (m *CAManager) LoadExisting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.caCert != nil && m.caKey != nil {
		return true
	}
	return m.loadExistingLocked() == nil
}

func (m *CAManager) loadExistingLocked() error {
	certPEM, err := os.ReadFile(m.certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(m.keyPath)
	if err != nil {
		return err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return errors.New("无法解析 CA 证书 PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return err
	}
	if time.Now().After(cert.NotAfter) {
		return errors.New("CA 证书已过期")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return errors.New("无法解析 CA 私钥 PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	m.setCALocked(cert, key, certBlock.Bytes)
	return nil
}

func (m *CAManager) generateNewLocked() error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return fmt.Errorf("创建证书目录失败: %w", err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成 CA 私钥失败: %w", err)
	}
	serial, err := randomSerialNumber()
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   caCommonName,
			Organization: []string{caOrganization},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(caValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("签发自签名 Root CA 失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(m.keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("保存 CA 私钥失败: %w", err)
	}
	if err := os.WriteFile(m.certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("保存 CA 证书失败: %w", err)
	}
	m.setCALocked(cert, priv, der)

	m.leafMu.Lock()
	clear(m.leafCache)
	m.leafMu.Unlock()
	return nil
}

func (m *CAManager) setCALocked(cert *x509.Certificate, key *ecdsa.PrivateKey, der []byte) {
	m.caCert = cert
	m.caKey = key
	m.caDER = append([]byte(nil), der...)
	sum256 := sha256.Sum256(der)
	sum1 := sha1.Sum(der)
	m.sha256FP = strings.ToUpper(hex.EncodeToString(sum256[:]))
	m.sha1FP = strings.ToUpper(hex.EncodeToString(sum1[:]))
}

// CertPool 返回包含当前 Root CA 的证书池（主要用于单元测试验证 TLS 握手）。
func (m *CAManager) CertPool() (*x509.CertPool, error) {
	if err := m.EnsureCA(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	cert := m.caCert
	m.mu.RUnlock()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, nil
}

// CertForHost 为指定域名或 IP 动态签发并缓存短期 TLS 叶证书。
func (m *CAManager) CertForHost(host string) (*tls.Certificate, error) {
	clean := NormalizeHostPattern(host)
	if clean == "" {
		return nil, errors.New("目标主机名为空")
	}

	m.leafMu.RLock()
	if cached, ok := m.leafCache[clean]; ok {
		m.leafMu.RUnlock()
		return cached, nil
	}
	m.leafMu.RUnlock()

	if err := m.EnsureCA(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	caCert := m.caCert
	caKey := m.caKey
	caDER := m.caDER
	m.mu.RUnlock()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   clean,
			Organization: []string{caOrganization},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(0, 0, leafValidityDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(clean); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{clean}
	}

	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("签发域名证书失败 (%s): %w", clean, err)
	}
	leafCert, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, err
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
		Leaf:        leafCert,
	}

	m.leafMu.Lock()
	if len(m.leafCache) >= maxCachedLeaves {
		clear(m.leafCache)
	}
	m.leafCache[clean] = tlsCert
	m.leafMu.Unlock()
	return tlsCert, nil
}

// Status 检查本机 Root CA 的生成与操作系统根证书信任状态。
func (m *CAManager) Status() CertStatus {
	_ = m.LoadExisting()

	m.mu.RLock()
	generated := m.caCert != nil
	certPath := m.certPath
	keyPath := m.keyPath
	fp256 := m.sha256FP
	fp1 := m.sha1FP
	var notAfter time.Time
	if m.caCert != nil {
		notAfter = m.caCert.NotAfter
	}
	m.mu.RUnlock()

	st := CertStatus{
		Generated:         generated,
		CertPath:          certPath,
		KeyPath:           keyPath,
		CommonName:        caCommonName,
		FingerprintSHA256: fp256,
		FingerprintSHA1:   fp1,
		NotAfter:          notAfter,
		InstallPromptHint: platformInstallPromptHint(),
	}
	if generated {
		st.EnvSnippets = buildEnvSnippets(certPath)
		installed, trusted, detail := checkSystemCATrust(certPath, fp256, fp1)
		st.Installed = installed
		st.Trusted = trusted
		st.StatusDetail = detail
	} else {
		st.StatusDetail = "尚未生成本机专属自签名根证书，点击“一键安装并信任证书”将自动生成并导入系统"
	}
	return st
}

// InstallAndTrust 确保本机 Root CA 已生成，并调用操作系统原生授权流程一键导入并设为受信任根证书。
func (m *CAManager) InstallAndTrust() (CertStatus, error) {
	if err := m.EnsureCA(); err != nil {
		return m.Status(), err
	}
	if err := installSystemCATrust(m.certPath); err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
}

// Uninstall 从操作系统根证书存储中卸载并移除当前 Root CA。
func (m *CAManager) Uninstall() (CertStatus, error) {
	if !m.LoadExisting() {
		return m.Status(), nil
	}
	m.mu.RLock()
	certPath := m.certPath
	fp256 := m.sha256FP
	fp1 := m.sha1FP
	m.mu.RUnlock()

	if err := uninstallSystemCATrust(certPath, fp256, fp1); err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
}

func buildEnvSnippets(certPath string) []EnvSnippet {
	return buildEnvSnippetsForOS(certPath, runtime.GOOS)
}

func buildEnvSnippetsForOS(certPath, goos string) []EnvSnippet {
	if goos == "windows" {
		return []EnvSnippet{
			{Label: "Node.js (PowerShell)", Value: fmt.Sprintf(`$env:NODE_EXTRA_CA_CERTS="%s"`, certPath)},
			{Label: "Python / curl (PowerShell)", Value: fmt.Sprintf(`$env:SSL_CERT_FILE="%s"; $env:REQUESTS_CA_BUNDLE="%s"`, certPath, certPath)},
			{Label: "Git (使用系统证书库)", Value: `git config --global http.sslBackend schannel`},
		}
	}
	return []EnvSnippet{
		{Label: "Node.js", Value: fmt.Sprintf(`export NODE_EXTRA_CA_CERTS=%q`, certPath)},
		{Label: "Python / OpenSSL CLI", Value: fmt.Sprintf(`export SSL_CERT_FILE=%q REQUESTS_CA_BUNDLE=%q`, certPath, certPath)},
		{Label: "Git (临时生效)", Value: fmt.Sprintf(`git -c http.sslCAInfo=%q status`, certPath)},
	}
}

// matchWindowsCertutilFingerprint 忽略空白与冒号差异，校验 certutil -verifystore 输出是否包含目标证书指纹。
func matchWindowsCertutilFingerprint(output, sha256FP, sha1FP string) bool {
	if sha1FP == "" && sha256FP == "" {
		return strings.TrimSpace(output) != ""
	}
	r := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", ":", "")
	compact := strings.ToUpper(r.Replace(output))
	if sha1FP != "" && strings.Contains(compact, strings.ToUpper(sha1FP)) {
		return true
	}
	if sha256FP != "" && strings.Contains(compact, strings.ToUpper(sha256FP)) {
		return true
	}
	return false
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if n.Sign() <= 0 {
		n = big.NewInt(1)
	}
	return n, nil
}
