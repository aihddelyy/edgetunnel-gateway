package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/version"
)

type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

type GitHubRelease struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	PublishedAt string         `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
}

type CheckResult struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	HasUpdate      bool   `json:"hasUpdate"`
	ReleaseTitle   string `json:"releaseTitle"`
	ReleaseNotes   string `json:"releaseNotes"`
	PublishedAt    string `json:"publishedAt"`
	TargetAsset    string `json:"targetAsset"`
	DownloadURL    string `json:"downloadUrl"`
	AssetSize      int64  `json:"assetSize"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
}

// CheckForUpdate 查询 GitHub 官方 Releases 仓库的最新发布版本
func CheckForUpdate(proxyURL string) (*CheckResult, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", version.RepoOwner, version.RepoName)

	client := createHTTPClient(proxyURL, 15*time.Second)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "EdgeTunnel-LB-Updater")
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GitHub API 返回状态码 %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 数据失败: %w", err)
	}

	targetAsset, downloadURL, assetSize := matchAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)

	hasUpdate := isNewerVersion(rel.TagName, version.CurrentVersion)

	res := &CheckResult{
		CurrentVersion: version.CurrentVersion,
		LatestVersion:  rel.TagName,
		HasUpdate:      hasUpdate,
		ReleaseTitle:   rel.Name,
		ReleaseNotes:   rel.Body,
		PublishedAt:    rel.PublishedAt,
		TargetAsset:    targetAsset,
		DownloadURL:    downloadURL,
		AssetSize:      assetSize,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
	}

	return res, nil
}

// ApplyUpdate 下载目标安装包并执行原子更新与平滑自重启
func ApplyUpdate(downloadURL string, useMirror bool, proxyURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("下载链接不能为空")
	}

	// 1. 获取当前程序执行绝对路径
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前程序路径失败: %w", err)
	}
	realExePath, err := filepath.EvalSymlinks(exePath)
	if err == nil {
		exePath = realExePath
	}

	exeDir := filepath.Dir(exePath)
	tmpPath := filepath.Join(exeDir, fmt.Sprintf(".update_%d.tmp", time.Now().UnixNano()))
	bakPath := filepath.Join(exeDir, fmt.Sprintf(".backup_%d.bak", time.Now().UnixNano()))

	// 2. 准备下载 URLs (支持镜像加速与直连重试)
	var tryURLs []string
	if useMirror && strings.HasPrefix(downloadURL, "https://github.com/") {
		tryURLs = append(tryURLs, "https://ghproxy.net/"+downloadURL)
		tryURLs = append(tryURLs, "https://ghfast.top/"+downloadURL)
	}
	tryURLs = append(tryURLs, downloadURL)

	logger.Infof("[Updater] 开始下载更新文件，准备尝试 %d 个下载源...", len(tryURLs))

	var downloadErr error
	downloadSuccess := false

	client := createHTTPClient(proxyURL, 10*time.Minute)

	for idx, urlStr := range tryURLs {
		logger.Infof("[Updater] 尝试第 %d/%d 个下载源: %s", idx+1, len(tryURLs), urlStr)

		err := downloadFile(client, urlStr, tmpPath)
		if err == nil {
			downloadSuccess = true
			logger.Infof("[Updater] ✅ 文件下载完成并校验通过: %s", tmpPath)
			break
		}
		logger.Warnf("[Updater] 源 %s 下载失败: %v", urlStr, err)
		downloadErr = err
		_ = os.Remove(tmpPath)
	}

	if !downloadSuccess {
		return fmt.Errorf("全部下载源均失败: %v", downloadErr)
	}

	// 3. 赋予可执行权限
	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("赋予新版本文件执行权限失败: %w", err)
	}

	// 4. 原子覆盖替换
	_ = os.Remove(bakPath)
	if err := os.Rename(exePath, bakPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("备份原程序失败 (可能缺少目录写入权限): %w", err)
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		// 覆盖失败，立即安全回滚原程序
		_ = os.Rename(bakPath, exePath)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换新版本可执行文件失败，已自动回滚原版本: %w", err)
	}

	// 替换成功，清理备份
	_ = os.Remove(bakPath)
	logger.Infof("🎉 [Updater] 二进制替换成功，目标路径: %s", exePath)

	// 5. 延迟 1 秒后触发自重启
	go func() {
		time.Sleep(1 * time.Second)
		logger.Infof("🚀 [Updater] 正在拉起新版本进程并安全平滑退出...")
		RestartSelf()
	}()

	return nil
}

// RestartSelf 统一重启网关进程
func RestartSelf() {
	exe, err := os.Executable()
	if err != nil {
		logger.Errorf("[Restart] 获取程序路径失败: %v", err)
		os.Exit(1)
	}
	realExe, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = realExe
	}

	args := os.Args[1:]
	if runtime.GOOS == "windows" {
		cmdStr := fmt.Sprintf("timeout /t 1 /nobreak >nul & start \"\" \"%s\"", exe)
		for _, a := range args {
			cmdStr += fmt.Sprintf(" \"%s\"", a)
		}
		_ = exec.Command("cmd.exe", "/c", cmdStr).Start()
	} else {
		// Linux / OpenWrt: 切换到可执行文件所在物理目录，延迟 1 秒释放端口后后台自启动
		cmdArgs := ""
		for _, a := range args {
			cmdArgs += fmt.Sprintf(" '%s'", a)
		}
		exeDir := filepath.Dir(exe)
		logPath := filepath.Join(exeDir, "app.log")
		shCmd := fmt.Sprintf("sleep 1 && cd '%s' && nohup '%s'%s >> '%s' 2>&1 &", exeDir, exe, cmdArgs, logPath)
		_ = exec.Command("/bin/sh", "-c", shCmd).Start()
	}
	os.Exit(0)
}

func downloadFile(client *http.Client, urlStr, destPath string) error {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "EdgeTunnel-LB-Updater")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP 响应状态码 %d", resp.StatusCode)
	}

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	// 限制最大下载 150MB，防止异常
	written, err := io.Copy(out, io.LimitReader(resp.Body, 150*1024*1024))
	if err != nil {
		return err
	}

	// 校验大小：有效 Go 二进制通常至少 > 2MB
	if written < 1024*1024 {
		return fmt.Errorf("下载的文件体积异常 (%d 字节)，可能下载到了错误网页", written)
	}

	return nil
}

func matchAsset(assets []ReleaseAsset, goos, goarch string) (name string, downloadURL string, size int64) {
	expectedPrefix := "edgetunnel-lb-" + goos + "-" + goarch
	if goos == "windows" {
		expectedPrefix += ".exe"
	}

	for _, a := range assets {
		if a.Name == expectedPrefix {
			return a.Name, a.DownloadURL, a.Size
		}
	}

	// 如果没有严格全字匹配，退而求其次匹配包含关系
	for _, a := range assets {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, goos) && strings.Contains(lower, goarch) && !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".zip") {
			return a.Name, a.DownloadURL, a.Size
		}
	}

	return "", "", 0
}

func isNewerVersion(latest, current string) bool {
	lClean := strings.TrimPrefix(strings.TrimSpace(latest), "v")
	cClean := strings.TrimPrefix(strings.TrimSpace(current), "v")

	if lClean == "" || cClean == "" {
		return false
	}
	if lClean == cClean {
		return false
	}

	lParts := strings.Split(lClean, ".")
	cParts := strings.Split(cClean, ".")

	maxLen := len(lParts)
	if len(cParts) > maxLen {
		maxLen = len(cParts)
	}

	for i := 0; i < maxLen; i++ {
		lNum := 0
		cNum := 0
		if i < len(lParts) {
			lNum, _ = strconv.Atoi(lParts[i])
		}
		if i < len(cParts) {
			cNum, _ = strconv.Atoi(cParts[i])
		}

		if lNum > cNum {
			return true
		}
		if lNum < cNum {
			return false
		}
	}

	return false
}

func createHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
