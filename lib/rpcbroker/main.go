package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"rpcbroker/server"
	"strings"
	"syscall"
	"time"
)

func main() {

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exePath, _ := os.Executable()
	BINPATH := filepath.Dir(exePath)
	log.Printf("BINPATH = [%s]\n", BINPATH)

	LD_LIBRARY_PATH := os.Getenv("LD_LIBRARY_PATH")
	if strings.TrimSpace(LD_LIBRARY_PATH) == "" {

		LIB := filepath.Join(BINPATH, "lib")
		MEDIASRV := filepath.Join(BINPATH, "lib/mediasrv")
		MEDIASRV_LIB := filepath.Join(BINPATH, "lib/mediasrv/lib")
		EXTENDS_LIB := filepath.Join(BINPATH, "extends")

		LD_LIBRARY_PATH = fmt.Sprintf("%s:%s:%s:%s", LIB, MEDIASRV, MEDIASRV_LIB, EXTENDS_LIB)
		// LD_LIBRARY_PATH = filepath.Join(BINPATH, "lib/mediasrv")
	}

	LOG_LEVEL := os.Getenv("LOG_LEVEL")
	if strings.TrimSpace(LOG_LEVEL) == "" {
		LOG_LEVEL = "info"
	}

	MEDIA_DIR := os.Getenv("MEDIA_DIR")
	if strings.TrimSpace(MEDIA_DIR) == "" {
		MEDIA_DIR = filepath.Join(BINPATH, "data/media")
	}

	WEB_PORT := os.Getenv("WEB_PORT")
	if strings.TrimSpace(WEB_PORT) == "" {
		WEB_PORT = "8005"
	}

	dbDir := filepath.Join(BINPATH, "data/database")

	if err := os.MkdirAll(dbDir, 0755); err != nil {
		panic("数据目录创建失败")
	}

	if err := os.MkdirAll(filepath.Join(MEDIA_DIR, "default"), 0755); err != nil {
		panic("数据目录创建失败")
	}

	envs := os.Environ()
	envs = append(envs, fmt.Sprintf("LD_LIBRARY_PATH=%s", LD_LIBRARY_PATH))
	envs = append(envs, fmt.Sprintf("LOG_LEVEL=%s", LOG_LEVEL))
	envs = append(envs, fmt.Sprintf("MEDIA_DIR=%s", MEDIA_DIR))

	// cmd := exec.Command(filepath.Join(BINPATH, "bin/mediasrv"), "-o", "/var/log/mediasrv.log", "-a", "/var/run/mediasrv.socket")
	os.MkdirAll("/usr/trim/etc/", 0755) // 这个目录必须存在，否则下面的命令会报错
	mediasrv := exec.CommandContext(ctx, filepath.Join(BINPATH, "bin/mediasrv"), "-a", "/var/run/mediasrv.socket")
	mediasrv.Env = append(os.Environ(), envs...)
	mediasrv.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}

	w := io.MultiWriter(os.Stdout)
	mediasrv.Stdout = w
	mediasrv.Stderr = w

	if err := mediasrv.Start(); err != nil {
		panic("mediasrv 服务启动失败")
	}

	pgid := mediasrv.Process.Pid
	defer func() {
		stop()
		syscall.Kill(-pgid, syscall.SIGTERM)

	}()

	go func() {
		// 回收mediasrv进程
		mediasrv.Wait()
	}()

	log.Println("mediasrv 服务启动成功")

	// 中间层
	go func() {
		s := server.NewServer(envs)
		VOLUMN_PATH := "/vol1/1000"

		os.RemoveAll(filepath.Dir(VOLUMN_PATH))
		os.MkdirAll(filepath.Dir(VOLUMN_PATH), 0755)

		if err := os.Symlink(MEDIA_DIR, VOLUMN_PATH); err != nil {
			panic(fmt.Errorf("[%s -> /vol1/1000]软链接创建失败", MEDIA_DIR))
		}

		entrys, err := os.ReadDir(MEDIA_DIR)
		if err != nil {
			panic(err)
		}

		dirs := make([]string, 0)
		for _, entry := range entrys {
			name := entry.Name()
			if entry.IsDir() && name != "mediasrv.transcode" {
				dirs = append(dirs, filepath.Join(VOLUMN_PATH, name))
			}
		}

		if len(dirs) == 0 {
			s.InitData(VOLUMN_PATH)
		} else {
			s.InitData(strings.Join(dirs, ":"))
		}
		s.Start()
	}()

	log.Println("启动自定义服务成功")

	// 等待两秒服务启动
	time.Sleep(2 * time.Second)

	metaDir := filepath.Join(BINPATH, "data/@appmeta/trim.media")
	args := []string{
		fmt.Sprintf("--port=%s", WEB_PORT),
		fmt.Sprintf("--static=%s", filepath.Join(BINPATH, "trim.media")),
		"--trim-appname=trim.media",
		"--trim-username=trim-media",
		fmt.Sprintf("--root=%s", filepath.Join(BINPATH, "data")),
		fmt.Sprintf("--meta=%s", metaDir),
	}

	dbPath := filepath.Join(dbDir, "trimmedia.db")
	initSqlPath := filepath.Join(BINPATH, "init.sql")
	log.Printf("initSqlPath=%q metaDir=%q", initSqlPath, metaDir)
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Println("初始化数据库中....")

			cmd := exec.Command("sed", "-i", fmt.Sprintf("s|/vol1/@appmeta/trim.media|%s|g", metaDir), initSqlPath)
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Fatalf("替换脚本失败: %s", string(out))
				panic(err)
			}

			cmd = exec.Command("sed", "-i", fmt.Sprintf("s|/vol1|%s/|g", MEDIA_DIR), initSqlPath)
			out, err = cmd.CombinedOutput()
			if err != nil {
				log.Fatalf("替换脚本失败: %s", string(out))
				panic(err)
			}

			cmd = exec.Command("sqlite3", dbPath, ".read "+initSqlPath)
			out, err = cmd.CombinedOutput()
			if err != nil {
				log.Fatalf("数据库初始化失败：%s", string(out))
				panic(err)
			}

		}
	}

	trimMedia := exec.CommandContext(ctx, filepath.Join(BINPATH, "trim.media/trim-media"), args...)

	trimMedia.Env = append(os.Environ(), envs...)

	trimMedia.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid, Pdeathsig: syscall.SIGTERM}
	trimMedia.Cancel = func() error {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}

	if err := trimMedia.Run(); err != nil {
		if ctx.Err() != nil {
			log.Println("trim-media 已随程序关闭") // 是我们主动杀的，不是失败
		} else {
			log.Printf("服务启动失败: %v", err) // 真正的异常退出
		}
	}

}
