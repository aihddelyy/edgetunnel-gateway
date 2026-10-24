package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// CleanMirrorURL 标准化镜像 URL，确保以 http/https 开头且以 / 结尾
func CleanMirrorURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	if !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	return raw
}

var defaultMirrors = []string{
	"https://ghfast.top/",
	"https://ghproxy.net/",
}

// CheckForUpdate 查询 GitHub 官方 Releases 仓库的最新发布版本 (支持自定义镜像与镜像回退)
func CheckForUpdate(proxyURL string, customMirror string) (*CheckResult, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", version.RepoOwner, version.RepoName)

	client := createHTTPClient(proxyURL, 10*time.Second)
	req, err := http.NewRequest("GET", apiURL, nil)
	var rel GitHubRelease
	var fetchErr error
	success := false

	if err == nil {
		req.Header.Set("User-Agent", "EdgeTunnel-LB-Updater")
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if err := json.NewDecoder(resp.Body).Decode(&rel); err == nil && rel.TagName != "" {
					success = true
				} else {
					fetchErr = fmt.Errorf("解析 Release 数据失败")
				}
			} else {
				bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				fetchErr = fmt.Errorf("GitHub API 返回状态码 %d: %s", resp.StatusCode, string(bodyBytes))
			}
		} else {
			fetchErr = err
		}
	} else {
		fetchErr = err
	}

	// 若官方 API 失败，尝试镜像通道探测最新版本
	if !success {
		logger.Warnf("[Updater] GitHub 官方 API 检索失败 (%v)，正在启动镜像多通道版本探测...", fetchErr)
		var mirrorCandidates []string
		if cm := CleanMirrorURL(customMirror); cm != "" {
			mirrorCandidates = append(mirrorCandidates, cm)
		}
		for _, dm := range defaultMirrors {
			if CleanMirrorURL(customMirror) != dm {
				mirrorCandidates = append(mirrorCandidates, dm)
			}
		}

		for _, m := range mirrorCandidates {
			// 1. 尝试镜像 302 重定向探测
			tag := probeTagViaRedirect(client, m)
			if tag != "" {
				logger.Infof("[Updater] ✅ 通过镜像 (%s) 探测到最新发布 Tag: %s", m, tag)
				rel = GitHubRelease{
					TagName:     tag,
					Name:        fmt.Sprintf("Release %s (通过加速镜像获取)", tag),
					Body:        "（由于网络环境限制，当前通过加速镜像同步检测到此新版本）",
					PublishedAt: time.Now().Format(time.RFC3339),
				}
				success = true
				break
			}

			// 2. 尝试镜像 raw 代码版本探测
			tag = probeTagViaRawVersion(client, m)
			if tag != "" {
				logger.Infof("[Updater] ✅ 通过镜像 (%s) Raw 代码探测到最新版本: %s", m, tag)
				rel = GitHubRelease{
					TagName:     tag,
					Name:        fmt.Sprintf("Release %s (版本探测)", tag),
					Body:        "（通过加速镜像同步检测到此新版本）",
					PublishedAt: time.Now().Format(time.RFC3339),
				}
				success = true
				break
			}
		}
	}

	if !success {
		return nil, fmt.Errorf("检查更新失败 (官方 API 与加速镜像均不可达): %w", fetchErr)
	}

	targetAsset, downloadURL, assetSize := matchAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)

	// 若是从镜像探测得到且未带 assets，则自动推导标准产物下载地址
	if downloadURL == "" && rel.TagName != "" {
		targetAsset = "edt-gateway-" + runtime.GOOS + "-" + runtime.GOARCH
		if runtime.GOOS == "windows" {
			targetAsset += ".exe"
		}
		downloadURL = fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
			version.RepoOwner, version.RepoName, rel.TagName, targetAsset)
	}

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

// probeTagViaRedirect 通过镜像请求 releases/latest 获取 302 重定向中的 tag
func probeTagViaRedirect(client *http.Client, mirror string) string {
	latestURL := mirror + fmt.Sprintf("https://github.com/%s/%s/releases/latest", version.RepoOwner, version.RepoName)
	noRedirectClient := &http.Client{
		Transport: client.Transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest("GET", latestURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "EdgeTunnel-LB-Updater")
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusSeeOther {
		loc := resp.Header.Get("Location")
		re := regexp.MustCompile(`/releases/tag/([^/?#]+)`)
		if m := re.FindStringSubmatch(loc); len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// probeTagViaRawVersion 通过镜像获取远端代码中的 version.go 解析版本号
func probeTagViaRawVersion(client *http.Client, mirror string) string {
	rawURL := mirror + fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/go_version/pkg/version/version.go", version.RepoOwner, version.RepoName)
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "EdgeTunnel-LB-Updater")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		re := regexp.MustCompile(`CurrentVersion\s*=\s*"([^"]+)"`)
		if m := re.FindStringSubmatch(string(body)); len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// ApplyUpdate 下载目标安装包并执行原子更新与平滑自重启 (支持自定义镜像与多源重试)
func ApplyUpdate(downloadURL string, useMirror bool, customMirror string, proxyURL string) error {
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

	// 2. 准备下载 URLs (支持自定义镜像、内置镜像与直连多级回退)
	var tryURLs []string
	cleanCM := CleanMirrorURL(customMirror)

	if strings.HasPrefix(downloadURL, "https://github.com/") {
		if cleanCM != "" {
			tryURLs = append(tryURLs, cleanCM+downloadURL)
		}
		if useMirror {
			for _, dm := range defaultMirrors {
				if cleanCM != dm {
					tryURLs = append(tryURLs, dm+downloadURL)
				}
			}
		}
	}
	tryURLs = append(tryURLs, downloadURL)

	logger.Infof("[Updater] 开始下载更新文件，准备尝试 %d 个下载源...", len(tryURLs))

	var downloadErr error
	downloadSuccess := false

	client := createHTTPClient(proxyURL, 10*time.Minute)

	for idx, urlStr := range tryURLs {
		sourceLabel := "官方源"
		if cleanCM != "" && strings.HasPrefix(urlStr, cleanCM) {
			sourceLabel = "用户自定义镜像"
		} else if strings.Contains(urlStr, "ghproxy") || strings.Contains(urlStr, "ghfast") {
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

// isFakeIP 判定是否落入 RFC 2544 / RFC 5737 基准测试保留网段 198.18.0.0/15 (198.18.0.0 - 198.19.255.255)
// 该网段常被 Mihomo / Clash / Sing-box 用作 Fake-IP 地址池
func isFakeIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19)
}

// 纯净公共 DNS 服务器列表（绕过本机被劫持的 53/1053 端口）
var publicDNSList = []string{
	"223.5.5.5:53",   // 阿里公共 DNS
	"119.29.29.29:53", // 腾讯 DNSPod
	"180.76.76.76:53", // 百度 DNS
	"1.1.1.1:53",      // Cloudflare
	"8.8.8.8:53",      // Google
}

// resolveDomainClean 防 Fake-IP 穿透解析
func resolveDomainClean(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if isFakeIP(ip) {
			return nil, fmt.Errorf("目标地址为 Fake-IP 保留段 (%s)，无法直连公网路由", host)
		}
		return []string{host}, nil
	}

	// 1. 先尝试用系统默认 DNS 解析
	defaultResolver := net.DefaultResolver
	ips, err := defaultResolver.LookupIPAddr(ctx, host)
	hasFakeIP := false
	var validIPs []string

	if err == nil && len(ips) > 0 {
		for _, ipa := range ips {
			if isFakeIP(ipa.IP) {
				hasFakeIP = true
			} else {
				validIPs = append(validIPs, ipa.IP.String())
			}
		}
	}

	if !hasFakeIP && len(validIPs) > 0 {
		return validIPs, nil
	}

	if hasFakeIP {
		logger.Warnf("[Updater/DNS] 检测到系统 DNS 返回 Fake-IP (198.18.x.x) 污染: 域名 %s，正在启用公网纯净 DNS 穿透解析...", host)
	}

	// 2. 依次向公共纯净 DNS 请求真实 IP
	for _, dnsServer := range publicDNSList {
		r := &net.Resolver{
			PreferGo: true,
			Dial: func(dialCtx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(dialCtx, "udp", dnsServer)
			},
		}
		cleanIPs, dnsErr := r.LookupIPAddr(ctx, host)
		if dnsErr == nil && len(cleanIPs) > 0 {
			var cleanList []string
			for _, cip := range cleanIPs {
				if !isFakeIP(cip.IP) {
					cleanList = append(cleanList, cip.IP.String())
				}
			}
			if len(cleanList) > 0 {
				logger.Infof("[Updater/DNS] ✅ 域名 %s 通过纯净 DNS (%s) 解析成功: %v", host, dnsServer, cleanList)
				return cleanList, nil
			}
		}
	}

	if len(validIPs) > 0 {
		return validIPs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("解析域名 %s 失败 (系统 DNS 与公共 DNS 均无法解析): %w", host, err)
	}
	return nil, fmt.Errorf("域名 %s 仅解析到 Fake-IP，无法建立直连", host)
}

// cleanDialContext 自定义纯净直连拨号器，杜绝 Fake-IP 掉坑
func cleanDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}

	ips, err := resolveDomainClean(ctx, host)
	if err != nil {
		// 回退到普通拨号
		d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	}

	dialer := net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	var lastErr error
	for _, ipStr := range ips {
		targetAddr := net.JoinHostPort(ipStr, port)
		conn, dialErr := dialer.DialContext(ctx, network, targetAddr)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}

	return nil, fmt.Errorf("连接 %s (%v) 失败: %w", addr, ips, lastErr)
}

func createHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         cleanDialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
			// 配置了专属代理时由代理端接管解析与转发
			transport.DialContext = (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
