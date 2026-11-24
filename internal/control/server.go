package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
	"github.com/KevinXC5/flyshadow-bar/internal/core"
	"github.com/KevinXC5/flyshadow-bar/internal/engine"
	"github.com/KevinXC5/flyshadow-bar/internal/version"
)

const runtimeFileName = "flyshadow-bar.run.json"

// RuntimeInfo 写入 ~/.FlyShadow/flyshadow-bar.run.json，供 CLI 定位运行中的 GUI 实例。
type RuntimeInfo struct {
	PID       int    `json:"pid"`
	Addr      string `json:"addr"`
	Token     string `json:"token"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
}

// DefaultRuntimeFilePath 返回运行时发现文件路径。
func DefaultRuntimeFilePath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("FLYSHADOW_BAR_RUN_FILE")); custom != "" {
		return custom, nil
	}
	dir, err := config.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runtimeFileName), nil
}

// Server 是内置在 GUI 进程中的本地 HTTP/JSON 控制服务（仅监听 127.0.0.1）。
type Server struct {
	eng         *engine.Engine
	runtimePath string
	token       string
	addr        string
	ln          net.Listener
	httpSrv     *http.Server
	onShowUI    func()
	onQuit      func()
}

// ServerOptions 控制服务配置项。
type ServerOptions struct {
	RuntimePath string
	ListenAddr  string // 默认 127.0.0.1:0
	OnShowUI    func()
	OnQuit      func()
}

// StartServer 启动本地控制服务并写入运行时发现文件。
func StartServer(eng *engine.Engine, opts ServerOptions) (*Server, error) {
	listenAddr := opts.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	rtPath := opts.RuntimePath
	if rtPath == "" {
		var err error
		rtPath, err = DefaultRuntimeFilePath()
		if err != nil {
			return nil, err
		}
	}

	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("监听本地控制端口失败: %w", err)
	}

	s := &Server{
		eng:         eng,
		runtimePath: rtPath,
		token:       token,
		addr:        ln.Addr().String(),
		ln:          ln,
		onShowUI:    opts.OnShowUI,
		onQuit:      opts.OnQuit,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", s.auth(s.handleStatus))
	mux.HandleFunc("/api/v1/nodes", s.auth(s.handleNodes))
	mux.HandleFunc("/api/v1/node", s.auth(s.handleSelectNode))
	mux.HandleFunc("/api/v1/mode", s.auth(s.handleMode))
	mux.HandleFunc("/api/v1/udp", s.auth(s.handleUDP))
	mux.HandleFunc("/api/v1/native-udp", s.auth(s.handleNativeUDP))
	mux.HandleFunc("/api/v1/sysproxy", s.auth(s.handleSysProxy))
	mux.HandleFunc("/api/v1/ping", s.auth(s.handlePing))
	mux.HandleFunc("/api/v1/reload", s.auth(s.handleReload))
	mux.HandleFunc("/api/v1/connect", s.auth(s.handleConnect))
	mux.HandleFunc("/api/v1/disconnect", s.auth(s.handleDisconnect))
	mux.HandleFunc("/api/v1/rules", s.auth(s.handleRules))
	mux.HandleFunc("/api/v1/rules/toggle", s.auth(s.handleRuleToggle))
	mux.HandleFunc("/api/v1/connections", s.auth(s.handleConnections))
	mux.HandleFunc("/api/v1/connections/close", s.auth(s.handleCloseConnection))
	mux.HandleFunc("/api/v1/show", s.auth(s.handleShow))
	mux.HandleFunc("/api/v1/quit", s.auth(s.handleQuit))

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := s.writeRuntimeFile(); err != nil {
		_ = ln.Close()
		return nil, err
	}

	go func() {
		_ = s.httpSrv.Serve(ln)
	}()

	return s, nil
}

// Addr 返回实际监听的回环地址。
func (s *Server) Addr() string { return s.addr }

// Close 关闭本地控制服务并清理运行时发现文件。
func (s *Server) Close() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.httpSrv != nil {
		_ = s.httpSrv.Shutdown(ctx)
	}
	if s.runtimePath != "" {
		// 仅在文件仍指向当前 PID 时删除，避免误删新实例文件
		if info, err := ReadRuntimeFile(s.runtimePath); err == nil && info.PID == os.Getpid() {
			_ = os.Remove(s.runtimePath)
		}
	}
}

func (s *Server) writeRuntimeFile() error {
	info := RuntimeInfo{
		PID:       os.Getpid(),
		Addr:      s.addr,
		Token:     s.token,
		Version:   version.Version,
		StartedAt: time.Now().Unix(),
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.runtimePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.runtimePath, data, 0o600)
}

// ReadRuntimeFile 读取运行中的实例发现信息。
func ReadRuntimeFile(path string) (*RuntimeInfo, error) {
	if path == "" {
		var err error
		path, err = DefaultRuntimeFilePath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	if info.Addr == "" || info.Token == "" {
		return nil, errors.New("运行时文件不完整")
	}
	return &info, nil
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未授权的控制请求"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": s.eng.Snapshot().Nodes,
	})
}

type nodeRequest struct {
	Query  string `json:"query"`
	NodeID int    `json:"nodeId"`
}

func (s *Server) handleSelectNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req nodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
		return
	}
	if req.NodeID > 0 {
		if err := s.eng.SelectNode(req.NodeID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.eng.Snapshot())
		return
	}
	if _, err := s.eng.SelectNodeByQuery(req.Query); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

type modeRequest struct {
	Mode string `json:"mode"`
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req modeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效"})
		return
	}
	mode, err := core.ParseProxyMode(req.Mode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.eng.SetMode(mode); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

type toggleRequest struct {
	Action string `json:"action"` // on | off | toggle
}

func resolveToggle(action string, current bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "on", "true", "1", "enable":
		return true, nil
	case "off", "false", "0", "disable":
		return false, nil
	case "toggle", "":
		return !current, nil
	default:
		return current, fmt.Errorf("无效开关参数 %q（支持: on, off, toggle）", action)
	}
}

func (s *Server) handleUDP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req toggleRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	next, err := resolveToggle(req.Action, s.eng.Snapshot().UDPAccelerationEnabled)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.eng.SetUDPAcceleration(next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handleNativeUDP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req toggleRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	next, err := resolveToggle(req.Action, s.eng.Snapshot().NativeUDP)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.eng.SetNativeUDP(next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handleSysProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req toggleRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	next, err := resolveToggle(req.Action, s.eng.Snapshot().SystemProxy)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.eng.SetSystemProxy(next); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	ranked, err := s.eng.PingAll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": ranked})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	if err := s.eng.Reload(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.Connect(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.Disconnect(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot())
}

type ruleIDRequest struct {
	ID int `json:"id"`
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snap := s.eng.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{
			"rules":        snap.CustomRules,
			"labels":       snap.RuleLabels,
			"activeLabels": snap.ActiveRuleLabels,
		})
	case http.MethodPost:
		var rule config.DomainRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "规则格式无效"})
			return
		}
		added, err := s.eng.AddCustomRule(rule)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, added)
	case http.MethodPut:
		var rule config.DomainRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "规则格式无效"})
			return
		}
		updated, err := s.eng.UpdateCustomRule(rule)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		var req ruleIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的规则 ID"})
			return
		}
		if err := s.eng.DeleteCustomRule(req.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持的请求方法"})
	}
}

func (s *Server) handleRuleToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req ruleIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少有效的规则 ID"})
		return
	}
	enabled, err := s.eng.ToggleCustomRule(req.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "enabled": enabled})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.Connections()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": list})
}

type closeConnRequest struct {
	Key string `json:"key"`
}

func (s *Server) handleCloseConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return
	}
	var req closeConnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Key) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少连接 key"})
		return
	}
	s.eng.CloseConnection(req.Key)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	if s.onShowUI != nil {
		s.onShowUI()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	if s.onQuit != nil {
		go s.onQuit()
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
