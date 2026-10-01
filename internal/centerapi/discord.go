package centerapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alfaos/alfaos/internal/config"
	"github.com/alfaos/alfaos/internal/logging"
)

type loginNotifyRequest struct {
	Event string `json:"event"`
	User  string `json:"user"`
	Host  string `json:"host"`
	From  string `json:"from"`
}

func (s *Server) handleNotifyLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
		return
	}
	var req loginNotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
		return
	}
	if req.Event == "" {
		req.Event = "login"
	}
	if req.User == "" {
		req.User = "alfaos"
	}

	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	s.cfg = cfg

	webhook := strings.TrimSpace(cfg.Notify.DiscordWebhook)
	if webhook == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"sent":    false,
			"warning": "notify.discord_webhook empty in host config",
		})
		return
	}

	content := fmt.Sprintf(
		"**ALFAOS RDP %s**\n• user: `%s`\n• host: `%s`\n• from: `%s`\n• time: %s",
		req.Event, req.User, req.Host, req.From, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	if err := PostDiscordWebhook(webhook, content); err != nil {
		logging.Warn("Discord webhook failed: %v", err)
		writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	logging.Info("Discord notified: RDP %s user=%s from=%s", req.Event, req.User, req.From)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": true})
}

// PostDiscordWebhook sends a plain content message to a Discord webhook URL.
func PostDiscordWebhook(webhookURL, content string) error {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return fmt.Errorf("empty webhook url")
	}
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// NotifyDiscordTest posts a one-shot install/test message from the host.
func NotifyDiscordTest(cfg *config.Config) error {
	webhook := strings.TrimSpace(cfg.Notify.DiscordWebhook)
	if webhook == "" {
		return fmt.Errorf("notify.discord_webhook is empty")
	}
	msg := fmt.Sprintf(
		"**ALFAOS notify ready**\nHost will alert this channel on successful RDP login.\n• vm: `%s`\n• time: %s",
		cfg.VM.Name, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	return PostDiscordWebhook(webhook, msg)
}
