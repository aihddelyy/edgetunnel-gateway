//go:build !windows

package updater

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func runRestartProcess(exe string, args []string) error {
	exeDir := filepath.Dir(exe)
	logPath := filepath.Join(exeDir, "app.log")

	var cmdArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "-config" || a == "--config") && i+1 < len(args) {
			cfgPath := args[i+1]
			if !filepath.IsAbs(cfgPath) {
				cfgPath = filepath.Join(exeDir, cfgPath)
			}
			cmdArgs = append(cmdArgs, a, fmt.Sprintf("'%s'", cfgPath))
			i++
			continue
		}
		cmdArgs = append(cmdArgs, fmt.Sprintf("'%s'", a))
	}

	argStr := strings.Join(cmdArgs, " ")
	if argStr != "" {
		argStr = " " + argStr
	}

	// 针对 OpenWrt / Linux 的核心优化：
	// 1. 绝不调用 nohup (BusyBox 默认没有 nohup 命令)
	// 2. 延迟 2 秒让 Linux 内核完全释放 8787/8788/8789 端口，避免 bind 冲突
	// 3. 使用 (sleep 2; cd ...; exe ...) & 原生 POSIX 子 shell 后台派生
	// 4. 重定向标准输入为 /dev/null，避免随父进程终止断开
	// 5. 设置 Setsid: true 彻底脱离父进程会话组，成为孤儿进程由 PID 1 接管
	shCmd := fmt.Sprintf("(sleep 2; cd '%s'; '%s'%s >> '%s' 2>&1) </dev/null >/dev/null 2>&1 &", exeDir, exe, argStr, logPath)
	cmd := exec.Command("/bin/sh", "-c", shCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
