package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// newUpstreamTransport 构造指向 trim-music unix socket 的 Transport。
// 移动端 API 层（/mobile/api/v1）与 1:1 透传反代共用它，上游永远是本机 socket。
func newUpstreamTransport(socketPath string) *http.Transport {
	return &http.Transport{
		DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(c, "unix", socketPath)
		},
	}
}

// serveProxy 监听 listenAddr：
//   - /mobile/api/v1/*  → 移动端高级 API 门面（见 mobile_api.go，统一鉴权/响应格式）
//   - 其余（/music/* 等）→ 1:1 反向代理到 unix://socketPath，
//     行为等价 caddy 的 `reverse_proxy unix//var/run/trim_music.socket`（含 X-Real-IP / X-Forwarded-For），
//     保证现有 Web 端 / 老版移动端零改动。
func serveProxy(ctx context.Context, listenAddr, socketPath string) {
	transport := newUpstreamTransport(socketPath)

	legacy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "trim.music"
			ip, _, _ := net.SplitHostPort(r.In.RemoteAddr)
			if strings.TrimSpace(ip) == "" {
				ip = r.In.RemoteAddr
			}
			r.Out.Header.Set("X-Real-IP", ip)
			r.Out.Header.Set("X-Forwarded-For", ip)
		},
	}

	mux := http.NewServeMux()
	mux.Handle(mobileAPIPrefix+"/", mobileAPIHandler(transport))
	mux.Handle("/", legacy)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("http proxy listening on %s -> unix://%s (mobile api at %s/api/v1)\n", listenAddr, socketPath, mobileAPIPrefix)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http proxy serve error: %v", err)
	}
}
