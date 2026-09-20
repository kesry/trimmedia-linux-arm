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
	"strings"
	"syscall"
)

func main() {

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exePath, _ := os.Executable()
	BINPATH := filepath.Dir(exePath)
	LD_LIBRARY_PATH := os.Getenv("LD_LIBRARY_PATH")
	if strings.TrimSpace(LD_LIBRARY_PATH) == "" {
		LD_LIBRARY_PATH = fmt.Sprintf("%s", filepath.Join(BINPATH, "lib"))
	}
	log.Printf("BINPATH = [%s], LD_LIBRARY_PATH = [%s]\n", BINPATH, LD_LIBRARY_PATH)

	LOG_LEVEL := os.Getenv("LOG_LEVEL")
	if strings.TrimSpace(LOG_LEVEL) == "" {
		LOG_LEVEL = "info"
	}

	MEDIA_DIR := os.Getenv("MEDIA_DIR")
	if strings.TrimSpace(MEDIA_DIR) == "" {
		log.Println("unset env MEDIA_DIR, use default /vol1/1000")
		MEDIA_DIR = "/vol1/1000"
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
			log.Println("初始化数据库中....")

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
			log.Println("database init success.")
		}
	}

	w := io.MultiWriter(os.Stdout)

	log.Println("staring media server...")
	trimMedia := exec.CommandContext(ctx, filepath.Join(BINPATH, "trim.media/trim-media"), args...)
	trimMedia.Env = append(os.Environ(), fmt.Sprintf("LD_LIBRARY_PATH=%s", LD_LIBRARY_PATH))
	trimMedia.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	trimMedia.Start()
	log.Println("staring media server success")

	trimMedia.Stdout = w
	trimMedia.Stderr = w

	pgid := trimMedia.Process.Pid
	trimMedia.Cancel = func() error {
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}

	defer func() {
		stop()
		syscall.Kill(-pgid, syscall.SIGTERM)
	}()

	if err := trimMedia.Wait(); err != nil {
		if ctx.Err() != nil {
			log.Println("trim-media 已随程序关闭") // 是我们主动杀的，不是失败
		} else {
			log.Printf("服务启动失败: %v", err) // 真正的异常退出
		}
	}

}
