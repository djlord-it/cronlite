package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// EasyCron webhook payload
type WebhookPayload struct {
	JobID       string `json:"job_id"`
	ExecutionID string `json:"execution_id"`
	ScheduledAt string `json:"scheduled_at"`
	FiredAt     string `json:"fired_at"`
}

// EasyCron job response (partial)
type JobResponse struct {
	Name string `json:"name"`
}

// Telegram sendMessage request
type TelegramMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

var (
	botToken    string
	chatID      string
	easycronURL string
	easycronKey string
	httpClient  = &http.Client{Timeout: 10 * time.Second}
)

func main() {
	botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID = os.Getenv("TELEGRAM_CHAT_ID")
	easycronURL = os.Getenv("EASYCRON_URL")
	easycronKey = os.Getenv("EASYCRON_API_KEY")

	if botToken == "" || chatID == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
	}

	addr := ":9090"
	if v := os.Getenv("PORT"); v != "" {
		addr = ":" + v
	} else if v := os.Getenv("ADDR"); v != "" {
		addr = v
	}

	http.HandleFunc("/webhook", webhookHandler)
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	log.Printf("telegram-bridge listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Parse fired_at for friendly display
	firedAt := payload.FiredAt
	if t, err := time.Parse(time.RFC3339, payload.FiredAt); err == nil {
		firedAt = t.Format("15:04 UTC")
	}

	// Look up job name from EasyCron
	jobName := lookupJobName(payload.JobID)

	// Check if there's a ?check= URL to fetch
	parsed, _ := url.Parse(r.RequestURI)
	checkURL := parsed.Query().Get("check")

	var text string
	if checkURL != "" {
		// Fetch the check URL and report results
		status, summary := fetch(checkURL)
		icon := "✅"
		if status < 200 || status >= 400 {
			icon = "❌"
		}
		text = fmt.Sprintf(
			"%s %s\n%s\n\n%s",
			icon, jobName, firedAt, summary,
		)
	} else {
		text = fmt.Sprintf("🔔 %s fired at %s", jobName, firedAt)
	}

	if err := sendTelegram(text); err != nil {
		log.Printf("telegram send failed: %v", err)
		http.Error(w, "telegram error", http.StatusBadGateway)
		return
	}

	log.Printf("forwarded %s (%s) to telegram", jobName, payload.ExecutionID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"forwarded":true}`)
}

// lookupJobName fetches the job name from EasyCron API. Falls back to a
// truncated job ID if the lookup fails.
func lookupJobName(jobID string) string {
	if easycronURL == "" || easycronKey == "" {
		return shortID(jobID)
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/jobs/%s", easycronURL, jobID), nil)
	if err != nil {
		return shortID(jobID)
	}
	req.Header.Set("Authorization", "Bearer "+easycronKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return shortID(jobID)
	}
	defer resp.Body.Close()

	var job JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil || job.Name == "" {
		return shortID(jobID)
	}
	return job.Name
}

// fetch GETs a URL and returns (status_code, one-line summary).
func fetch(target string) (int, string) {
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return 0, fmt.Sprintf("bad url: %v", err)
	}
	req.Header.Set("User-Agent", "easycron-bridge/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Sprintf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := string(raw)

	// Try to parse as JSON and summarize
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err == nil {
		return resp.StatusCode, summarizeJSON(data)
	}

	// Plain text — first meaningful line
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "<") {
			if len(line) > 200 {
				line = line[:200]
			}
			return resp.StatusCode, line
		}
	}

	return resp.StatusCode, fmt.Sprintf("HTTP %d (no readable body)", resp.StatusCode)
}

// summarizeJSON extracts a human-readable summary from common JSON formats.
func summarizeJSON(data map[string]interface{}) string {
	// GitHub/Atlassian status page format
	if status, ok := data["status"].(map[string]interface{}); ok {
		if desc, ok := status["description"].(string); ok {
			return desc
		}
	}

	// wttr.in weather format
	if cc, ok := data["current_condition"].([]interface{}); ok && len(cc) > 0 {
		if c, ok := cc[0].(map[string]interface{}); ok {
			desc := "?"
			if wd, ok := c["weatherDesc"].([]interface{}); ok && len(wd) > 0 {
				if d, ok := wd[0].(map[string]interface{}); ok {
					desc, _ = d["value"].(string)
				}
			}
			temp, _ := c["temp_C"].(string)
			feels, _ := c["FeelsLikeC"].(string)
			return fmt.Sprintf("%s, %s°C (feels %s°C)", desc, temp, feels)
		}
	}

	// Generic: show top-level string/number values
	var parts []string
	for k, v := range data {
		switch val := v.(type) {
		case string:
			if len(val) < 80 {
				parts = append(parts, fmt.Sprintf("%s: %s", k, val))
			}
		case float64:
			parts = append(parts, fmt.Sprintf("%s: %g", k, val))
		case bool:
			parts = append(parts, fmt.Sprintf("%s: %v", k, val))
		}
		if len(parts) >= 4 {
			break
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " | ")
	}

	return fmt.Sprintf("{%d keys}", len(data))
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sendTelegram(text string) error {
	msg := TelegramMessage{
		ChatID: chatID,
		Text:   text,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
