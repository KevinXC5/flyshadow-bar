package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KevinXC5/flyshadow-bar/internal/config"
)

func TestCloudClientSubscribeAndRuleSync(t *testing.T) {
	const testSecret = "mock-sub-secret-001"
	wantAuth := base64.StdEncoding.EncodeToString([]byte(testSecret))

	var savedRule config.DomainRule
	var updatedRule config.DomainRule
	var removedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/subscribe":
			if got := r.URL.Query().Get("password"); got != testSecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"node": []map[string]any{
					{"id": 10, "name": "HK-01", "cnName": "香港 01", "host": "hk.example.invalid", "port": 443, "enableQuic": 1, "nativeUdp": 1},
				},
				"user": map[string]any{
					"trafficSize":     214748364800,
					"trafficUpload":   1073741824,
					"trafficDownload": 53687091200,
					"level":           3,
					"expireTime":      "2026-12-07 10:30:16",
				},
				"customRule": []map[string]any{
					{"id": 1, "domain": "cn", "matching": 6, "proxyType": 0},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/custom/rule/list":
			if got := r.Header.Get("subscribe-password"); got != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    200,
				"message": "ok",
				"data": []map[string]any{
					{"id": 88, "enable": 1, "domain": "openai.com", "matching": 1, "proxyType": 2, "label": "ai", "createTime": "2026-11-24 20:00:00"},
				},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/custom/rule/save":
			if got := r.Header.Get("subscribe-password"); got != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&savedRule)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})

		case r.Method == http.MethodPost && r.URL.Path == "/custom/rule/update":
			if got := r.Header.Get("subscribe-password"); got != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&updatedRule)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/custom/rule/remove/"):
			if got := r.Header.Get("subscribe-password"); got != wantAuth {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			removedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli := &Client{BaseURLs: []string{srv.URL}, DisableDoH: true}
	ctx := context.Background()

	sub, err := cli.FetchSubscribe(ctx, testSecret, "")
	if err != nil {
		t.Fatalf("FetchSubscribe failed: %v", err)
	}
	if len(sub.Nodes) != 1 || sub.Nodes[0].ID != 10 {
		t.Fatalf("unexpected nodes: %+v", sub.Nodes)
	}
	if sub.User == nil || sub.User.Level != 3 || sub.User.ExpireTime <= 0 {
		t.Fatalf("unexpected user info: %+v", sub.User)
	}

	rules, err := cli.FetchCustomRules(ctx, testSecret, "")
	if err != nil || len(rules) != 1 || rules[0].ID != 88 || rules[0].CreateTime <= 0 {
		t.Fatalf("FetchCustomRules = (%+v, %v)", rules, err)
	}

	if err := cli.SaveCustomRule(ctx, testSecret, config.DomainRule{
		ID:        999, // Save 必须自动清零 ID
		Domain:    "example.org",
		Matching:  config.MatchDomainSuffix,
		ProxyType: 2,
		Label:     "dev",
	}, ""); err != nil {
		t.Fatalf("SaveCustomRule failed: %v", err)
	}
	if savedRule.ID != 0 || savedRule.Domain != "example.org" {
		t.Fatalf("unexpected savedRule payload: %+v", savedRule)
	}

	if err := cli.UpdateCustomRule(ctx, testSecret, config.DomainRule{
		ID:        88,
		Domain:    "openai.com",
		Matching:  config.MatchDomainSuffix,
		ProxyType: 0,
		Enable:    config.IntPtr(0),
	}, ""); err != nil {
		t.Fatalf("UpdateCustomRule failed: %v", err)
	}
	if updatedRule.ID != 88 || updatedRule.IsEnabled() {
		t.Fatalf("unexpected updatedRule payload: %+v", updatedRule)
	}

	if err := cli.RemoveCustomRule(ctx, testSecret, 88, ""); err != nil {
		t.Fatalf("RemoveCustomRule failed: %v", err)
	}
	if removedPath != "/custom/rule/remove/88" {
		t.Fatalf("removedPath = %q", removedPath)
	}
}

func TestCloudClientErrorDoesNotLeakSecret(t *testing.T) {
	const secret = "ultra-secret-password-xyz"
	cli := &Client{BaseURLs: []string{"http://127.0.0.1:1"}, DisableDoH: true}
	_, err := cli.FetchSubscribe(context.Background(), secret, "")
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked subscribePassword: %v", err)
	}
}

func TestParseDoHTXTURLs(t *testing.T) {
	raw := []byte(`{"Answer":[{"type":16,"data":"\"https://a.example.invalid, https://b.example.invalid/ , invalid\""}]}`)
	got := parseDoHTXTURLs(raw)
	if len(got) != 2 || got[0] != "https://a.example.invalid" || got[1] != "https://b.example.invalid" {
		t.Fatalf("parseDoHTXTURLs = %v", got)
	}
}
