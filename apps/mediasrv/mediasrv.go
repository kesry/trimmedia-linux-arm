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
	CUST_LD_PRELOAD := os.Getenv("LD_PRELOAD")
	LD_PRELOAD_LIB := []string{}
	NODIRSO := filepath.Join(BINPATH, "lib/nodri.so")
	NODMAHEAPSO := filepath.Join(BINPATH, "lib/nodmaheap.so")
	NOCPUINFOSO := filepath.Join(BINPATH, "lib/fakecompat.so")
	if CUST_LD_PRELOAD != "" {
		LD_PRELOAD_LIB = append(LD_PRELOAD_LIB, CUST_LD_PRELOAD)
	}

	if _, err := os.Stat(NODIRSO); err == nil {
		LD_PRELOAD_LIB = append(LD_PRELOAD_LIB, NODIRSO)
	}

	if _, err := os.Stat(NODMAHEAPSO); err == nil {
		LD_PRELOAD_LIB = append(LD_PRELOAD_LIB, NODMAHEAPSO)
	}

	if _, err := os.Stat(NOCPUINFOSO); err == nil {
		LD_PRELOAD_LIB = append(LD_PRELOAD_LIB, NOCPUINFOSO)
	}

	envs := os.Environ()
	envs = append(envs, fmt.Sprintf("LD_LIBRARY_PATH=%s", LD_LIBRARY_PATH))

	if len(LD_PRELOAD_LIB) > 0 {
		envs = append(envs, fmt.Sprintf("LD_PRELOAD=%s", strings.Join(LD_PRELOAD_LIB, ":")))
	}

	mediasrv.Env = envs
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
