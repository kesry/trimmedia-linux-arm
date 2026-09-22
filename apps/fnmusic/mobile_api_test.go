package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// startFakeUpstream 在临时 unix socket 上起一个假 trim-music，断言收到 music-token Cookie，
// 并按路径返回 {code,msg,data} 封装。返回 socket 路径与清理函数。
func startFakeUpstream(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "fake.socket")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/music/api/v1/album/list", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "music-token=tk123") {
			t.Errorf("上游未收到注入的 Cookie: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"list":[{"guid":"a1","name":"X"}],"total":1}}`)
	})
	mux.HandleFunc("/music/api/v1/user/password-login", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if pw, _ := m["password"].(string); len(pw) != 64 {
			t.Errorf("登录密码未被 sha256（长度=%d）", len(pw))
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"userToken":"tk123","user":{"name":"admin"}}}`)
	})
	mux.HandleFunc("/music/api/v1/user/temp-token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"","data":{"tempToken":"tmp"}}`)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return sock, func() { _ = srv.Shutdown(context.Background()); _ = ln.Close() }
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMobileAPI(t *testing.T) {
	sock, cleanup := startFakeUpstream(t)
	defer cleanup()

	tr := newUpstreamTransport(sock)
	h := mobileAPIHandler(tr)
	base := mobileAPIPrefix + "/api/v1"

	t.Run("列表信封+token注入", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/albums?page=1&size=20", nil)
		req.Header.Set("X-Music-Token", "tk123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "" {
			t.Errorf("缺少 CORS 头")
		}
		var resp struct {
			Code int          `json:"code"`
			Data listEnvelope `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应非法 JSON: %v / %s", err, rec.Body.String())
		}
		if resp.Code != 0 || resp.Data.Total != 1 || resp.Data.Size != 20 {
			t.Errorf("信封错误: %+v", resp.Data)
		}
	})

	t.Run("登录聚合", func(t *testing.T) {
		rec := do(t, h, "POST", base+"/login", strings.NewReader(`{"username":"admin","password":"plain"}`))
		var resp struct {
			Code int `json:"code"`
			Data struct {
				Token     string `json:"token"`
				TempToken string `json:"tempToken"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("登录响应非法: %s", rec.Body.String())
		}
		if resp.Code != 0 || resp.Data.Token != "tk123" || resp.Data.TempToken != "tmp" {
			t.Errorf("登录聚合错误: %s", rec.Body.String())
		}
	})

	t.Run("OPTIONS预检", func(t *testing.T) {
		rec := do(t, h, "OPTIONS", base+"/albums", nil)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") == "" {
			t.Errorf("预检未按预期放行: %d", rec.Code)
		}
	})
	fmt.Println("mobile api smoke ok")
}
