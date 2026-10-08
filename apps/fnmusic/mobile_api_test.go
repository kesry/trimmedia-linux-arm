package main

import (
	"bytes"
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
	// 模拟真实曲库流接口：规范支持 Range/206，并显式给出 Content-Length。
	// 网关这一层必保留长度头，否则 <audio> 退化成「总长未知」浅缓冲流。
	mux.HandleFunc("/music/api/v1/track/stream", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "music-token=tk123") {
			t.Errorf("流接口上游未收到注入的 Cookie: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "audio/flac")
		if rng := r.Header.Get("Range"); rng != "" {
			if rng != "bytes=1000-1999" {
				t.Errorf("Range 未被透传: %q", rng)
			}
			w.Header().Set("Content-Range", "bytes 1000-1999/3000")
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(streamData(1000, 1000))
			return
		}
		w.Header().Set("Content-Length", "3000")
		_, _ = w.Write(streamData(3000, 0))
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

// streamData 生成 length 字节的可识别测试流（第 i 字节值为 offset+i，便于比对切片位置）。
func streamData(length, offset int) []byte {
	b := make([]byte, length)
	for i := range b {
		b[i] = byte(offset + i)
	}
	return b
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

	// 媒体流必须把总长递给客户端：Content-Length 一丢，<audio> 就只能下多少播多少。
	t.Run("播放流透传Content-Length", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/media/stream?guid=g1", nil)
		req.Header.Set("X-Music-Token", "tk123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码应为 200，实际 %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Length"); got != "3000" {
			t.Errorf("Content-Length 被网关丢了: %q", got)
		}
		if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("Accept-Ranges 丢了，客户端无法按字节区间 seek: %q", got)
		}
		if body := rec.Body.Bytes(); !bytes.Equal(body, streamData(3000, 0)) {
			t.Errorf("响应体不完整（%d 字节）", len(body))
		}
	})

	// 带 Range 的请求（<audio> seek / 预取尾段）：网关须透传 Range 并回 206 + 长度/区间头。
	t.Run("播放流透传Range并保留206长度", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/media/stream?guid=g1", nil)
		req.Header.Set("X-Music-Token", "tk123")
		req.Header.Set("Range", "bytes=1000-1999")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("状态码应为 206，实际 %d", rec.Code)
		}
		if got := rec.Header().Get("Content-Length"); got != "1000" {
			t.Errorf("206 的 Content-Length 被丢了: %q", got)
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes 1000-1999/3000" {
			t.Errorf("Content-Range 丢了，客户端无法把字节偏移映射到时间: %q", got)
		}
		if body := rec.Body.Bytes(); !bytes.Equal(body, streamData(1000, 1000)) {
			t.Errorf("206 响应体不是请求区间（%d 字节）", len(body))
		}
	})
	fmt.Println("mobile api smoke ok")
}
