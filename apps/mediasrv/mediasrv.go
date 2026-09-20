package main

import (
	"context"
	"fmt"
	"io"
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
	log.Printf("BINPATH = [%s]\n", BINPATH)

	LD_LIBRARY_PATH := os.Getenv("LD_LIBRARY_PATH")
	if strings.TrimSpace(LD_LIBRARY_PATH) == "" {
		LIB := filepath.Join(BINPATH, "lib")
		MEDIASRV := filepath.Join(BINPATH, "lib/mediasrv")
		MEDIASRV_LIB := filepath.Join(BINPATH, "lib/mediasrv/lib")
		EXTENDS_LIB := filepath.Join(BINPATH, "extends")

		LD_LIBRARY_PATH = fmt.Sprintf("%s:%s:%s:%s", LIB, MEDIASRV, MEDIASRV_LIB, EXTENDS_LIB)
	}
	log.Printf("BINPATH = [%s], LD_LIBRARY_PATH = [%s]\n", BINPATH, LD_LIBRARY_PATH)


	os.MkdirAll("/usr/trim/etc/", 0755) // 这个目录必须存在，否则下面的命令会报错
	mediasrv := exec.CommandContext(ctx, filepath.Join(BINPATH, "bin/mediasrv"), "-a", "/var/run/mediasrv.socket")
	mediasrv.Env = append(os.Environ(), fmt.Sprintf("LD_LIBRARY_PATH=%s", LD_LIBRARY_PATH), fmt.Sprintf("LD_PRELOAD=%s", filepath.Join(BINPATH, "lib/nodri.so")))
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

	log.Println("mediasrv 服务启动成功")

	mediasrv.Wait()
}
