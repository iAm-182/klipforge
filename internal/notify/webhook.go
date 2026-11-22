package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 8 * time.Second}

// Send dispatches a notification to either a Discord Webhook or a generic n8n/HTTP Webhook.
// Safe to call asynchronously; silently ignores empty URLs.
func Send(webhookURL, eventType, title, body string) {
	url := strings.TrimSpace(webhookURL)
	if url == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
		return
	}

	var payload map[string]interface{}
	if strings.Contains(strings.ToLower(url), "discord.com/api/webhooks") || strings.Contains(strings.ToLower(url), "discordapp.com/api/webhooks") {
		msg := fmt.Sprintf("**%s**\n%s", title, body)
		if len(msg) > 1900 {
			msg = msg[:1900] + "..."
		}
		payload = map[string]interface{}{
			"username": "KlipForge Studio",
			"content":  msg,
		}
	} else {
		payload = map[string]interface{}{
			"source":    "klipforge",
			"event":     eventType,
			"title":     title,
			"message":   body,
			"timestamp": time.Now().Format(time.RFC3339),
		}
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}
