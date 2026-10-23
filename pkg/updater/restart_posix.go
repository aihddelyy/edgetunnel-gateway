//go:build !windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"edgetunnel-lb/pkg/logger"
)

func runRestartProcess(exe string, args []string) error {
	// 去除 Linux 下旧进程被 unlink 后带有的 " (deleted)" 标记
	exe = strings.TrimSuffix(strings.TrimSpace(exe), " (deleted)")
	exeDir := filepath.Dir(exe)
	logPath := filepath.Join(exeDir, "app.log")

	// 1. 处理所有命令行参数，特别是 -config 参数的路径绝对化
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

	// 确保二进制具备可执行权限
	_ = syscall.Chmod(exe, 0755)

	// 在 app.log 显式记录重启启动日志，方便用户随时 tail -f app.log 排查
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		fmt.Fprintf(f, "[%s] [Restart] 正在拉起新网关进程: %s%s\n",
			time.Now().Format("2006-01-02 15:04:05"), exe, argStr)
		_ = f.Close()
	}

	// 核心原理：
	// 1. 绝不使用末尾带 '&' 的嵌套子 shell 语法（外层 /bin/sh -c 退出时很多 ash 会清理后台子 job）；
	// 2. 直接让这个独立的 /bin/sh 进程执行 "sleep 2; cd ...; exec ... >> app.log 2>&1"；
	// 3. 设置 Setsid: true 独立会话，主进程 os.Exit(0) 后此 sh 进程彻底由系统的 PID 1 接管；
	// 4. 2 秒延迟确保 8787/8788/8789 端口被 Linux 内核完全释放回收；
	// 5. exec 原生替换为新二进制镜像，不残留中间 shell 进程。
	shScript := fmt.Sprintf("sleep 2; cd '%s'; exec '%s'%s >> '%s' 2>&1", exeDir, exe, argStr, logPath)
	cmd := exec.Command("/bin/sh", "-c", shScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	logger.Infof("[Restart] 发起自重启命令: %s", shScript)
	return cmd.Start()
}
