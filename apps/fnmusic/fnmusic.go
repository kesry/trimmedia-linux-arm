package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// trim-music 二进制不接收任何命令行参数，它把一系列路径写死在 /var/apps/trim.music 下，
// 因此只能靠创建软链接，把这些写死的固定路径指向本包内实际解出来的文件。
const (
	appHome     = "/var/apps/trim.music"       // 程序写死的应用主目录
	targetLink  = appHome + "/target"          // -> BINPATH/trim.music（含 trim-music、static、sqlite*.sql）
	pkgDirName  = "trim.music"                 // install.sh 下载解压出来的包目录名
	dbDir       = appHome + "/var/db"          // 数据库目录（写死）
	dbPath      = dbDir + "/music.db"          // 数据库文件（写死）
	binName     = "trim-music"                 // 真正的服务二进制
	socketPath  = "/var/run/trim_music.socket" // trim-music 监听的 unix socket（写死）
	defaultPort = "8007"                      // 对外 HTTP 监听端口，可用 WEB_PORT 环境变量覆盖
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exePath, _ := os.Executable()
	BINPATH := filepath.Dir(exePath)
	log.Printf("BINPATH = [%s]\n", BINPATH)

	// trim-music 的动态库依赖很单纯（readelf -d 实测仅 libzmq.so.5 + libc.so.6，
	// ffprobe 走包内 wasm 版），不需要 mediasrv 那套 lib/mediasrv、extends 目录结构，
	// 只要把 libzmq.so.5 提取到本包 lib/ 下即可，libc 走系统路径。
	LD_LIBRARY_PATH := os.Getenv("LD_LIBRARY_PATH")
	if strings.TrimSpace(LD_LIBRARY_PATH) == "" {
		LD_LIBRARY_PATH = filepath.Join(BINPATH, "lib")
	}
	log.Printf("BINPATH = [%s], LD_LIBRARY_PATH = [%s]\n", BINPATH, LD_LIBRARY_PATH)

	pkgDir := filepath.Join(BINPATH, pkgDirName)

	// trim-music 读取的是写死路径，只能建软链接把 target 指回本包目录
	if err := os.MkdirAll(appHome, 0755); err != nil {
		panic(fmt.Sprintf("创建目录失败 %s: %v", appHome, err))
	}
	if err := symlinkForce(pkgDir, targetLink); err != nil {
		panic(fmt.Sprintf("软链接创建失败 %s -> %s: %v", targetLink, pkgDir, err))
	}
	log.Printf("target link ready: %s -> %s\n", targetLink, pkgDir)

	// trim-music 首次启动会用包内 sqlite*.sql 自行建 schema，并同步创建
	// meta/lyric-sqlite/lyric-xx.db 歌词分片库；如果抢先 sqlite3 建库（哪怕照抄官方 SQL），
	// 分片库不会按被跳过的升级流程补齐，trim-music 启动时会 panic:
	// unable to open database file: no such file or directory。
	// 所以这里只保证空目录存在，数据播种放到 schema 就绪之后进行。
	if err := os.MkdirAll(filepath.Join(appHome, "meta"), 0755); err != nil {
		panic(fmt.Sprintf("创建目录失败 %s: %v", filepath.Join(appHome, "meta"), err))
	}
	initSqlPath := filepath.Join(BINPATH, "init_data.sql")
	log.Printf("initSqlPath=%q dbPath=%q", initSqlPath, dbPath)

	binPath := filepath.Join(pkgDir, binName)
	_ = os.Chmod(binPath, 0755)

	w := io.MultiWriter(os.Stdout)

	log.Println("staring music server...")
	trimMusic := exec.CommandContext(ctx, binPath)
	trimMusic.Env = append(os.Environ(), fmt.Sprintf("LD_LIBRARY_PATH=%s", LD_LIBRARY_PATH))
	trimMusic.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	trimMusic.Stdout = w
	trimMusic.Stderr = w
	trimMusic.Start()
	log.Println("staring music server success")

	// schema 由 trim-music 初始化，就绪后再播种免登录数据（含 admin/oauth 与 app_state=initialized）
	go seedDB(ctx, initSqlPath)

	// 取代移植记录里的 caddy 反代：直接由 Go 监听 TCP 端口，把 HTTP 转发到 trim-music 的 unix socket，
	// 并保留 caddy 的 header_up X-Real-IP / X-Forwarded-For 行为。端口可用 WEB_PORT 环境变量调整。
	WEB_PORT := os.Getenv("WEB_PORT")
	if strings.TrimSpace(WEB_PORT) == "" {
		WEB_PORT = defaultPort
	}
	go serveProxy(ctx, fmt.Sprintf(":%s", WEB_PORT), socketPath)

	pgid := trimMusic.Process.Pid
	trimMusic.Cancel = func() error {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}

	defer func() {
		stop()
		syscall.Kill(-pgid, syscall.SIGTERM)
	}()

	if err := trimMusic.Wait(); err != nil {
		if ctx.Err() != nil {
			log.Println("trim-music 已随程序关闭") // 是我们主动杀的，不是失败
		} else {
			log.Printf("服务启动失败: %v", err) // 真正的异常退出
		}
	}
}

// seedDB 等待 trim-music 建好 schema 后，用 init_data.sql 播种免登录数据（只插数据、不建表）。
// 播种条件：app_state 表已存在（schema 初始化完成）且表为空（官方初始化会自己写入 server_guid，不会撞车）。
func seedDB(ctx context.Context, initSqlPath string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		out, err := exec.Command("sqlite3", dbPath, "SELECT COUNT(*) FROM app_state;").CombinedOutput()
		if err != nil { // schema 还没建好，继续等
			continue
		}
		if strings.TrimSpace(string(out)) != "0" { // 已有数据，无需播种
			return
		}
		log.Println("初始化数据库中....")
		out, err = exec.Command("sqlite3", dbPath, ".read "+initSqlPath).CombinedOutput()
		if err != nil {
			log.Fatalf("数据库初始化失败：%s", string(out))
		}
		log.Println("database init success.")
		return
	}
}

// symlinkForce 创建（或重建）软链接 link -> target；若 link 已存在且指向正确则跳过。
func symlinkForce(target, link string) error {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s 已存在且不是软链接", link)
		}
		if cur, err := os.Readlink(link); err == nil && cur == target {
			return nil
		}
		os.Remove(link)
	}
	return os.Symlink(target, link)
}

// serveProxy 监听 listenAddr，把所有 HTTP 请求反向代理到 unix://socketPath，
// 功能等价于 caddy 的 `reverse_proxy unix//var/run/trim_music.socket`（含 X-Real-IP / X-Forwarded-For）。
func serveProxy(ctx context.Context, listenAddr, socketPath string) {
	transport := &http.Transport{
		DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(c, "unix", socketPath)
		},
	}

	proxy := &httputil.ReverseProxy{
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

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           proxy,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("http proxy listening on %s -> unix://%s\n", listenAddr, socketPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http proxy serve error: %v", err)
	}
}
