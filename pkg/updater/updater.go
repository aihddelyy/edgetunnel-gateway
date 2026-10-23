package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/version"
)

type UpdateProgress struct {
	State       string `json:"state"`      // "idle", "downloading", "verifying", "replacing", "restarting", "error"
	Percent     int    `json:"percent"`    // 0 - 100
	Downloaded  int64  `json:"downloaded"` // bytes
	Total       int64  `json:"total"`      // bytes
	Speed       int64  `json:"speed"`      // bytes/sec
	SpeedStr    string `json:"speedStr"`   // e.g. "2.1 MB/s"
	Message     string `json:"message"`    // 详细提示
	Error       string `json:"error,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
}

var (
	progressMu      sync.RWMutex
	currentProgress = UpdateProgress{State: "idle", Message: "未在执行更新"}
)

// GetProgress 获取当前更新与下载进度状态
func GetProgress() UpdateProgress {
	progressMu.RLock()
	defer progressMu.RUnlock()
	return currentProgress
}

func setProgress(p UpdateProgress) {
	progressMu.Lock()
	defer progressMu.Unlock()
	p.UpdatedAt = time.Now().UnixMilli()
	currentProgress = p
}

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
		err := fmt.Errorf("下载链接不能为空")
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
	}

	setProgress(UpdateProgress{
		State:   "downloading",
		Percent: 1,
		Message: "正在解析下载源并建立网络连接...",
	})

	// 1. 获取当前程序执行绝对路径
	exePath, err := os.Executable()
	if err != nil {
		err = fmt.Errorf("获取当前程序路径失败: %w", err)
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
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
		sourceLabel := "官方源"
		if strings.Contains(urlStr, "ghproxy") || strings.Contains(urlStr, "ghfast") {
			sourceLabel = "国内加速镜像"
		}
		logger.Infof("[Updater] 尝试第 %d/%d 个下载源 (%s): %s", idx+1, len(tryURLs), sourceLabel, urlStr)

		setProgress(UpdateProgress{
			State:   "downloading",
			Percent: 2,
			Message: fmt.Sprintf("正在连接 %s (尝试第 %d/%d 个节点)...", sourceLabel, idx+1, len(tryURLs)),
		})

		err := downloadFile(client, urlStr, tmpPath, sourceLabel)
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
		err := fmt.Errorf("全部下载源均失败: %v", downloadErr)
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
	}

	// 3. 校验与赋予可执行权限
	setProgress(UpdateProgress{
		State:   "verifying",
		Percent: 99,
		Message: "安装包下载完成，正在校验完整性与执行权限...",
	})

	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		err = fmt.Errorf("赋予新版本文件执行权限失败: %w", err)
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
	}

	// 4. 原子覆盖替换
	setProgress(UpdateProgress{
		State:   "replacing",
		Percent: 99,
		Message: "正在执行安全原子替换 (覆盖旧二进制文件)...",
	})

	_ = os.Remove(bakPath)
	if err := os.Rename(exePath, bakPath); err != nil {
		_ = os.Remove(tmpPath)
		err = fmt.Errorf("备份原程序失败 (可能缺少目录写入权限): %w", err)
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		// 覆盖失败，立即安全回滚原程序
		_ = os.Rename(bakPath, exePath)
		_ = os.Remove(tmpPath)
		err = fmt.Errorf("替换新版本可执行文件失败，已自动回滚原版本: %w", err)
		setProgress(UpdateProgress{State: "error", Message: err.Error(), Error: err.Error()})
		return err
	}

	// 替换成功，清理备份
	_ = os.Remove(bakPath)
	logger.Infof("🎉 [Updater] 二进制替换成功，目标路径: %s", exePath)

	setProgress(UpdateProgress{
		State:   "restarting",
		Percent: 100,
		Message: "🎉 新版本原子替换成功！网关正在自动热重启...",
	})

	// 5. 延迟 1 秒后触发自重启 (传入已覆盖好的新可执行文件绝对路径)
	go func() {
		time.Sleep(1 * time.Second)
		logger.Infof("🚀 [Updater] 正在拉起新版本进程 (%s) 并平滑退出旧版本...", exePath)
		RestartExe(exePath, os.Args[1:])
	}()

	return nil
}

// RestartSelf 统一重启网关当前进程
func RestartSelf() {
	RestartExe("", os.Args[1:])
}

// RestartExe 重启指定绝对路径的可执行文件 (若为空则自动探测)
func RestartExe(targetExe string, args []string) {
	exe := targetExe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			logger.Errorf("[Restart] 获取程序路径失败: %v", err)
			os.Exit(1)
		}
		if realExe, err := filepath.EvalSymlinks(exe); err == nil {
			exe = realExe
		}
	}
	// 去除 Linux 下旧进程被 unlink 后带有的 " (deleted)" 标记
	exe = strings.TrimSuffix(strings.TrimSpace(exe), " (deleted)")

	if err := runRestartProcess(exe, args); err != nil {
		logger.Errorf("[Restart] 启动自重启后台任务失败: %v", err)
	}
	os.Exit(0)
}

type progressWriter struct {
	writer     io.Writer
	total      int64
	downloaded int64
	lastTime   time.Time
	lastBytes  int64
	onProgress func(downloaded, total, speed int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	if n > 0 {
		pw.downloaded += int64(n)
		now := time.Now()
		d := now.Sub(pw.lastTime)
		if d >= 200*time.Millisecond {
			bytesDiff := pw.downloaded - pw.lastBytes
			speed := int64(float64(bytesDiff) / d.Seconds())
			pw.lastTime = now
			pw.lastBytes = pw.downloaded
			if pw.onProgress != nil {
				pw.onProgress(pw.downloaded, pw.total, speed)
			}
		}
	}
	return n, err
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func downloadFile(client *http.Client, urlStr, destPath, sourceLabel string) error {
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

	totalSize := resp.ContentLength
	pw := &progressWriter{
		writer:    out,
		total:     totalSize,
		lastTime:  time.Now(),
		onProgress: func(downloaded, total, speed int64) {
			pct := 0
			if total > 0 {
				pct = int(float64(downloaded) / float64(total) * 100)
				if pct > 98 {
					pct = 98
				}
			}
			spStr := formatBytes(speed) + "/s"
			msg := fmt.Sprintf("正在拉取最新程序: %s / %s (速度: %s)", formatBytes(downloaded), formatBytes(total), spStr)
			setProgress(UpdateProgress{
				State:      "downloading",
				Percent:    pct,
				Downloaded: downloaded,
				Total:      total,
				Speed:      speed,
				SpeedStr:   spStr,
				Message:    msg,
			})
		},
	}

	// 限制最大下载 150MB，防止异常
	written, err := io.Copy(pw, io.LimitReader(resp.Body, 150*1024*1024))
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
	// 优先匹配新版标准产物命名 (edt-gateway-linux-amd64)
	primaryPrefix := "edt-gateway-" + goos + "-" + goarch
	// 兼容匹配历史版本产物命名 (edgetunnel-lb-linux-amd64)
	legacyPrefix := "edgetunnel-lb-" + goos + "-" + goarch

	if goos == "windows" {
		primaryPrefix += ".exe"
		legacyPrefix += ".exe"
	}

	// 1. 优先精确匹配 edt-gateway
	for _, a := range assets {
		if a.Name == primaryPrefix {
			return a.Name, a.DownloadURL, a.Size
		}
	}

	// 2. 兼容精确匹配 edgetunnel-lb
	for _, a := range assets {
		if a.Name == legacyPrefix {
			return a.Name, a.DownloadURL, a.Size
		}
	}

	// 3. 宽松模糊匹配 (包含平台与架构，排除压缩包)
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
