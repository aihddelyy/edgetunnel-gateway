package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
)

type TelegramSender struct {
	cfgMgr  *config.Manager
	metrics *model.Metrics
}

var globalSender *TelegramSender

func InitTelegram(cfgMgr *config.Manager, metrics *model.Metrics) *TelegramSender {
	s := &TelegramSender{
		cfgMgr:  cfgMgr,
		metrics: metrics,
	}
	globalSender = s
	return s
}

func GetTelegram() *TelegramSender {
	return globalSender
}

func (s *TelegramSender) getHTTPClient(proxyStr string) *http.Client {
	transport := &http.Transport{
		ResponseHeaderTimeout: 10 * time.Second,
	}

	if proxyStr != "" {
		pURL, err := url.Parse(proxyStr)
		if err == nil {
			if strings.HasPrefix(pURL.Scheme, "socks5") {
				dialer, err := proxy.FromURL(pURL, proxy.Direct)
				if err == nil {
					transport.Dial = dialer.Dial
				}
			} else {
				transport.Proxy = http.ProxyURL(pURL)
			}
		}
	}

	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}

type tgMessagePayload struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

func (s *TelegramSender) SendRawText(text string) error {
	cfg := s.cfgMgr.Get()
	token := strings.TrimSpace(cfg.Telegram.BotToken)
	chatID := config.CleanTelegramChatID(cfg.Telegram.ChatID)

	if token == "" || chatID == "" {
		return nil
	}

	apiHost := cfg.Telegram.APIHost
	if apiHost == "" {
		apiHost = "api.telegram.org"
	}

	reqURL := fmt.Sprintf("https://%s/bot%s/sendMessage", apiHost, token)
	payload := tgMessagePayload{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "Markdown",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	client := s.getHTTPClient(cfg.Telegram.Proxy)
	resp, err := client.Post(reqURL, "application/json", bytes.NewReader(data))
	if err != nil {
		logger.Warnf("[Telegram] 推送通知网络异常: %v", err)
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		logger.Warnf("[Telegram] API 返回错误 (%d): %s", resp.StatusCode, string(body))
		return fmt.Errorf("telegram status code: %d", resp.StatusCode)
	}

	if s.metrics != nil {
		atomic.AddInt64(&s.metrics.AlertsSent, 1)
	}
	return nil
}

func (s *TelegramSender) SendCircuitAlert(backend, reason string) {
	cfg := s.cfgMgr.Get()
	token := strings.TrimSpace(cfg.Telegram.BotToken)
	if token == "" {
		return
	}

	timeStr := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("⚠️ *【EdgeTunnel LB 节点熔断告警】*\n\n"+
		"• *故障节点*: `%s`\n"+
		"• *触发原因*: `%s`\n"+
		"• *告警时间*: `%s`\n"+
		"• *处置策略*: 自动隔离 60 秒并从轮询池摘除\n\n"+
		"💡 隔离期结束后将自动重新探活并平滑恢复。", backend, reason, timeStr)

	go func() {
		_ = s.SendRawText(msg)
	}()
}
