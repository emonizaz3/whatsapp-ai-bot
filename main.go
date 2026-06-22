package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Constants
const (
	defaultPort    = "8080"
	defaultModel   = "openai/gpt-oss-120b"
	maxLogsHistory = 200
)

// Dynamic Paths for persistence (Render hosting)
var (
	dataDir    = "."
	configPath = "config.json"
	logsPath   = "logs.json"
	dbPath     = "file:store.db?_foreign_keys=on"
)

// Structures
type Config struct {
	BotActive       bool     `json:"bot_active"`
	TargetNumbers   []string `json:"target_numbers"`
	GroqAPIKey      string   `json:"groq_api_key"`
	Model           string   `json:"model"`
	SystemPrompt    string   `json:"system_prompt"`
	CooldownMinutes int      `json:"cooldown_minutes"`
	DailyLimit      int      `json:"daily_limit"`
	EnableDelay     bool     `json:"enable_delay"`
	DelayMinSeconds int      `json:"delay_min_seconds"`
	DelayMaxSeconds int      `json:"delay_max_seconds"`
}

type TempConfig struct {
	BotActive       bool     `json:"bot_active"`
	TargetNumber    string   `json:"target_number"`
	TargetNumbers   []string `json:"target_numbers"`
	GroqAPIKey      string   `json:"groq_api_key"`
	Model           string   `json:"model"`
	SystemPrompt    string   `json:"system_prompt"`
	CooldownMinutes int      `json:"cooldown_minutes"`
	DailyLimit      int      `json:"daily_limit"`
	EnableDelay     bool     `json:"enable_delay"`
	DelayMinSeconds int      `json:"delay_min_seconds"`
	DelayMaxSeconds int      `json:"delay_max_seconds"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"` // INFO, SUCCESS, WARNING, ERROR
	Type      string `json:"type"`  // incoming_msg, outgoing_msg, bot_action, connection_status
	Target    string `json:"target,omitempty"`
	Message   string `json:"message"`
}

type ActiveReply struct {
	Target      string    `json:"target"`
	ScheduledAt time.Time `json:"scheduled_at"`
	SendAt      time.Time `json:"send_at"`
}

type TargetState struct {
	LastReplyTime time.Time           `json:"last_reply_time"`
	RepliesToday  int                 `json:"replies_today"`
	ChatHistory   []map[string]string `json:"chat_history"`
	CurrentDay    int                 `json:"current_day"`
}

// Global Variables
var (
	client    *whatsmeow.Client
	container *sqlstore.Container
	clientLog waLog.Logger
	
	// Target tracking states
	targetStates      = make(map[string]*TargetState)
	targetStatesMutex sync.Mutex

	// Active countdown scheduler states
	activeReplies      = make(map[string]ActiveReply)
	activeRepliesMutex sync.RWMutex
	
	// Config persistence state
	config      Config
	configMutex sync.RWMutex

	// Logs persistence state
	logsMutex sync.Mutex

	// WhatsApp connection state
	connStatus     string = "DISCONNECTED" // DISCONNECTED, CONNECTING, QR_CODE_READY, CONNECTED
	qrCodeString   string = ""
	connectedPhone string = ""
	whatsmeowMutex sync.Mutex
)

// Helpers
func cleanNumber(num string) string {
	num = strings.ReplaceAll(num, "+", "")
	num = strings.ReplaceAll(num, "-", "")
	num = strings.ReplaceAll(num, " ", "")
	return num
}

func cleanPhone(num string) string {
	var sb strings.Builder
	for _, r := range num {
		if r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return strings.TrimLeft(sb.String(), "0")
}

func phonesMatch(phone1, phone2 string) bool {
	p1 := cleanPhone(phone1)
	p2 := cleanPhone(phone2)
	if p1 == "" || p2 == "" {
		return false
	}
	if p1 == p2 {
		return true
	}
	// Suffix match check to handle country codes missing or present in config/JIDs.
	// Minimum match length of 9 digits covers standard mobile numbers (without country codes).
	const minMatchLen = 9
	if len(p1) >= minMatchLen && len(p2) >= minMatchLen {
		if len(p1) > len(p2) {
			return strings.HasSuffix(p1, p2)
		}
		return strings.HasSuffix(p2, p1)
	}
	return false
}

func getOrCreateTargetState(phone string) *TargetState {
	targetStatesMutex.Lock()
	defer targetStatesMutex.Unlock()

	phone = cleanNumber(phone)
	state, exists := targetStates[phone]
	if !exists {
		state = &TargetState{
			ChatHistory: make([]map[string]string, 0),
			CurrentDay:  time.Now().Day(),
		}
		targetStates[phone] = state
	}
	return state
}

func isTargetNumber(sender string, targets []string) bool {
	for _, t := range targets {
		if phonesMatch(sender, t) {
			return true
		}
	}
	return false
}

func resolveToPhoneJID(jid types.JID) types.JID {
	if jid.Server == "s.whatsapp.net" {
		return jid
	}
	if jid.Server == "lid" {
		pnJID, err := client.Store.LIDs.GetPNForLID(context.Background(), jid)
		if err == nil && !pnJID.IsEmpty() {
			addLog("INFO", "bot_action", jid.User, fmt.Sprintf("Resolved LID mapping to phone: %s", pnJID.User))
			return pnJID
		}

		// Try loading from server if mapping not found in local store
		infoMap, err := client.GetUserInfo(context.Background(), []types.JID{jid})
		if err == nil {
			if _, exists := infoMap[jid]; exists {
				pnJID, err := client.Store.LIDs.GetPNForLID(context.Background(), jid)
				if err == nil && !pnJID.IsEmpty() {
					addLog("INFO", "bot_action", jid.User, fmt.Sprintf("Resolved LID mapping to phone after query: %s", pnJID.User))
					return pnJID
				}
			}
		}
	}
	return jid
}

func loadConfig() {
	configMutex.Lock()
	defer configMutex.Unlock()

	data, err := os.ReadFile(configPath)
	if err == nil {
		var temp TempConfig
		if err := json.Unmarshal(data, &temp); err == nil {
			config.BotActive = temp.BotActive
			config.GroqAPIKey = temp.GroqAPIKey
			config.Model = temp.Model
			config.SystemPrompt = temp.SystemPrompt
			config.CooldownMinutes = temp.CooldownMinutes
			config.DailyLimit = temp.DailyLimit
			config.EnableDelay = temp.EnableDelay
			config.DelayMinSeconds = temp.DelayMinSeconds
			config.DelayMaxSeconds = temp.DelayMaxSeconds
			
			if len(temp.TargetNumbers) > 0 {
				config.TargetNumbers = temp.TargetNumbers
			} else if temp.TargetNumber != "" {
				config.TargetNumbers = []string{temp.TargetNumber}
			} else {
				config.TargetNumbers = []string{}
			}
			return
		}
	}

	// Default config if file read or parse fails
	initialTarget := cleanNumber(os.Getenv("TARGET_NUMBER"))
	var targets []string
	if initialTarget != "" {
		targets = []string{initialTarget}
	} else {
		targets = []string{}
	}

	config = Config{
		BotActive:       true,
		TargetNumbers:   targets,
		GroqAPIKey:      os.Getenv("GROQ_API_KEY"),
		Model:           defaultModel,
		SystemPrompt:    "তুমি আমার একজন কাছের বন্ধু। আমি তোমাকে বাংলায় যে মেসেজ দিব, তুমি তার রিপ্লাই দিবে একদম স্বাভাবিক ফ্রেন্ডলি বাংলায়। চ্যাটিংয়ের স্টাইলে উত্তর দিবে। তুমি আগের মেসেজগুলোর কন্টেক্সট মনে রেখে উত্তর দেবে। কোনো জ্ঞানগর্ভ উপদেশ দিবে না। এমনভাবে কথা বলবে যেন তুমি কোনো AI নও, রক্তে-মাংসে গড়া মানুষ।",
		CooldownMinutes: 10,
		DailyLimit:      2,
		EnableDelay:     true,
		DelayMinSeconds: 60,
		DelayMaxSeconds: 180,
	}
	saveConfigNoLock()
}

func saveConfigNoLock() {
	data, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		os.WriteFile(configPath, data, 0644)
	}
}

func saveConfig(newCfg Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	config = newCfg
	saveConfigNoLock()
}

func addLog(level, logType, target, message string) {
	logsMutex.Lock()
	defer logsMutex.Unlock()

	var entries []LogEntry
	data, err := os.ReadFile(logsPath)
	if err == nil {
		json.Unmarshal(data, &entries)
	}

	newEntry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     level,
		Type:      logType,
		Target:    cleanNumber(target),
		Message:   message,
	}
	entries = append(entries, newEntry)

	if len(entries) > maxLogsHistory {
		entries = entries[len(entries)-maxLogsHistory:]
	}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err == nil {
		os.WriteFile(logsPath, out, 0644)
	}

	// Output to stdout
	if target != "" {
		fmt.Printf("[%s] [%s] [%s] %s\n", level, logType, target, message)
	} else {
		fmt.Printf("[%s] [%s] %s\n", level, logType, message)
	}
}

func clearLogs() {
	logsMutex.Lock()
	defer logsMutex.Unlock()
	os.WriteFile(logsPath, []byte("[]"), 0644)
}

func getAIResponse(target string) string {
	configMutex.RLock()
	apiKey := config.GroqAPIKey
	modelName := config.Model
	systemPrompt := config.SystemPrompt
	configMutex.RUnlock()

	if apiKey == "" {
		addLog("WARNING", "bot_action", target, "GROQ_API_KEY পাওয়া যায়নি!")
		return "আমি এখন একটু ব্যস্ত ভাই, পরে কথা বলছি!"
	}

	url := "https://api.groq.com/openai/v1/chat/completions"

	state := getOrCreateTargetState(target)

	apiMessages := []map[string]string{
		{"role": "system", "content": systemPrompt},
	}
	apiMessages = append(apiMessages, state.ChatHistory...)

	payload := map[string]interface{}{
		"model":       modelName,
		"messages":    apiMessages,
		"temperature": 0.8,
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	addLog("INFO", "bot_action", target, fmt.Sprintf("Groq AI (%s) রিকোয়েস্ট পাঠানো হচ্ছে...", modelName))
	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		addLog("ERROR", "bot_action", target, fmt.Sprintf("AI API ত্রুটি: %v", err))
		return "একটু পরে কথা বলি দোস্ত, একটু ঝামেলায় আছি।"
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					state.ChatHistory = append(state.ChatHistory, map[string]string{"role": "assistant", "content": content})
					return content
				}
			}
		}
	}

	addLog("ERROR", "bot_action", target, fmt.Sprintf("AI API অবৈধ রেসপন্স: %s", string(body)))
	return "হুমম, পরে কথা হবে!"
}

func sendQueuedReply(target string, senderJID types.JID) {
	addLog("INFO", "bot_action", target, "AI রিপ্লাই জেনারেট করা হচ্ছে...")
	aiReply := getAIResponse(target)

	_, err := client.SendMessage(context.Background(), senderJID, &waE2E.Message{
		Conversation: proto.String(aiReply),
	})

	// Remove from active replies map
	activeRepliesMutex.Lock()
	delete(activeReplies, target)
	activeRepliesMutex.Unlock()

	state := getOrCreateTargetState(target)
	if err == nil {
		state.LastReplyTime = time.Now()
		state.RepliesToday++
		addLog("SUCCESS", "outgoing_msg", target, fmt.Sprintf("রিপ্লাই পাঠানো হয়েছে: %s (আজকের কাউন্ট: %d)", aiReply, state.RepliesToday))
	} else {
		addLog("ERROR", "bot_action", target, fmt.Sprintf("রিপ্লাই পাঠাতে ব্যর্থ: %v", err))
	}
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		if v.Info.IsFromMe || v.Info.IsGroup {
			return
		}

		configMutex.RLock()
		botActive := config.BotActive
		targets := config.TargetNumbers
		cooldownMin := config.CooldownMinutes
		dailyLimit := config.DailyLimit
		enableDelay := config.EnableDelay
		delayMin := config.DelayMinSeconds
		delayMax := config.DelayMaxSeconds
		configMutex.RUnlock()

		if !botActive {
			return
		}

		senderJID := v.Info.Sender
		resolvedJID := resolveToPhoneJID(senderJID)
		senderNum := resolvedJID.User

		incomingText := v.Message.GetConversation()
		if incomingText == "" && v.Message.GetExtendedTextMessage() != nil {
			incomingText = v.Message.GetExtendedTextMessage().GetText()
		}

		if incomingText == "" {
			return
		}

		addLog("INFO", "incoming_msg", senderNum, fmt.Sprintf("মেসেজ এসেছে: %s", incomingText))

		// Check target matching
		if isTargetNumber(senderNum, targets) {
			state := getOrCreateTargetState(senderNum)

			now := time.Now()
			if now.Day() != state.CurrentDay {
				state.RepliesToday = 0
				state.CurrentDay = now.Day()
			}

			// Save to chat history immediately to preserve context
			state.ChatHistory = append(state.ChatHistory, map[string]string{"role": "user", "content": incomingText})
			if len(state.ChatHistory) > 10 {
				state.ChatHistory = state.ChatHistory[len(state.ChatHistory)-10:]
			}

			// Daily limit check (only if limit > 0)
			if dailyLimit > 0 && state.RepliesToday >= dailyLimit {
				addLog("WARNING", "bot_action", senderNum, fmt.Sprintf("আজকের রিপ্লাইয়ের কোটা শেষ (%d/%d)। মেসেজ ইতিহাসে রাখা হলো, কিন্তু কোনো নতুন রিপ্লাই শিডিউল করা হবে না।", state.RepliesToday, dailyLimit))
				return
			}

			// Check if a reply is already queued
			activeRepliesMutex.RLock()
			_, alreadyScheduled := activeReplies[senderNum]
			activeRepliesMutex.RUnlock()

			if alreadyScheduled {
				addLog("INFO", "bot_action", senderNum, "ইতিমধ্যে একটি রিপ্লাই শিডিউল করা আছে। নতুন মেসেজটি ইতিহাসে (context) যোগ করা হলো।")
				return
			}

			// Cooldown check (only if cooldownMin > 0)
			cooldownActive := false
			var cooldownEnd time.Time
			if cooldownMin > 0 && !state.LastReplyTime.IsZero() {
				cooldownDuration := time.Duration(cooldownMin) * time.Minute
				cooldownEnd = state.LastReplyTime.Add(cooldownDuration)
				if now.Before(cooldownEnd) {
					cooldownActive = true
				}
			}

			if cooldownActive {
				delayDuration := cooldownEnd.Sub(now)
				addLog("INFO", "bot_action", senderNum, fmt.Sprintf("কুলডাউন চলছে। কুলডাউন শেষ হওয়ার পর (%v পর) একটি একত্রিত রিপ্লাই শিডিউল করা হলো।", delayDuration.Round(time.Second)))

				// Schedule reply at cooldownEnd
				activeRepliesMutex.Lock()
				activeReplies[senderNum] = ActiveReply{
					Target:      senderNum,
					ScheduledAt: now,
					SendAt:      cooldownEnd,
				}
				activeRepliesMutex.Unlock()

				go func() {
					time.Sleep(delayDuration)

					activeRepliesMutex.RLock()
					_, stillScheduled := activeReplies[senderNum]
					activeRepliesMutex.RUnlock()
					if !stillScheduled {
						return
					}

					sendQueuedReply(senderNum, v.Info.Sender)
				}()
			} else {
				// Normal reply delay calculation
				delaySeconds := 0
				if enableDelay {
					diff := delayMax - delayMin
					if diff <= 0 {
						delaySeconds = delayMin
					} else {
						delaySeconds = rand.Intn(diff) + delayMin
					}
					addLog("INFO", "bot_action", senderNum, fmt.Sprintf("%d সেকেন্ড পর AI রিপ্লাই পাঠানো হবে...", delaySeconds))
				} else {
					addLog("INFO", "bot_action", senderNum, "বিলম্ব নিষ্ক্রিয়। অবিলম্বে AI রিপ্লাই জেনারেট করা হচ্ছে...")
				}

				delayDuration := time.Duration(delaySeconds) * time.Second

				activeRepliesMutex.Lock()
				activeReplies[senderNum] = ActiveReply{
					Target:      senderNum,
					ScheduledAt: now,
					SendAt:      now.Add(delayDuration),
				}
				activeRepliesMutex.Unlock()

				go func() {
					if delayDuration > 0 {
						time.Sleep(delayDuration)
					}

					activeRepliesMutex.RLock()
					_, stillScheduled := activeReplies[senderNum]
					activeRepliesMutex.RUnlock()
					if !stillScheduled {
						return
					}

					sendQueuedReply(senderNum, v.Info.Sender)
				}()
			}
		} else {
			addLog("INFO", "bot_action", senderNum, "মেসেজটি টার্গেট নাম্বারের নয়।")
		}
	}
}

func connectWhatsApp() {
	whatsmeowMutex.Lock()
	if client.IsConnected() {
		whatsmeowMutex.Unlock()
		return
	}
	connStatus = "CONNECTING"
	whatsmeowMutex.Unlock()

	addLog("INFO", "connection_status", "", "WhatsApp সংযোগ করার চেষ্টা করা হচ্ছে...")

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(context.Background())
		if err != nil {
			whatsmeowMutex.Lock()
			connStatus = "DISCONNECTED"
			whatsmeowMutex.Unlock()
			addLog("ERROR", "connection_status", "", fmt.Sprintf("QR কোড চ্যানেল পেতে ব্যর্থ: %v", err))
			return
		}

		err = client.Connect()
		if err != nil {
			whatsmeowMutex.Lock()
			connStatus = "DISCONNECTED"
			whatsmeowMutex.Unlock()
			addLog("ERROR", "connection_status", "", fmt.Sprintf("কানেকশন ব্যর্থ: %v", err))
			return
		}

		go func() {
			for evt := range qrChan {
				if evt.Event == "code" {
					whatsmeowMutex.Lock()
					qrCodeString = evt.Code
					connStatus = "QR_CODE_READY"
					whatsmeowMutex.Unlock()

					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
					addLog("INFO", "connection_status", "", "QR কোড প্রস্তুত। দয়া করে স্ক্যান করুন।")
				} else if evt.Event == "success" {
					whatsmeowMutex.Lock()
					qrCodeString = ""
					connStatus = "CONNECTED"
					if client.Store.ID != nil {
						connectedPhone = client.Store.ID.User
					}
					whatsmeowMutex.Unlock()
					addLog("SUCCESS", "connection_status", "", fmt.Sprintf("WhatsApp সফলভাবে সংযুক্ত হয়েছে! ইউজার: %s", connectedPhone))
				} else if evt.Event == "timeout" {
					whatsmeowMutex.Lock()
					qrCodeString = ""
					connStatus = "DISCONNECTED"
					whatsmeowMutex.Unlock()
					addLog("WARNING", "connection_status", "", "QR কোড স্ক্যান করার সময় শেষ (Timeout)।")
				}
			}
		}()
	} else {
		err := client.Connect()
		if err != nil {
			whatsmeowMutex.Lock()
			connStatus = "DISCONNECTED"
			whatsmeowMutex.Unlock()
			addLog("ERROR", "connection_status", "", fmt.Sprintf("কানেকশন ব্যর্থ: %v", err))
			return
		}

		whatsmeowMutex.Lock()
		qrCodeString = ""
		connStatus = "CONNECTED"
		if client.Store.ID != nil {
			connectedPhone = client.Store.ID.User
		}
		whatsmeowMutex.Unlock()
		addLog("SUCCESS", "connection_status", "", fmt.Sprintf("WhatsApp সংযুক্ত হয়েছে (পূর্বে নিবন্ধিত ডিভাইস)! ইউজার: %s", connectedPhone))
	}
}

// HTTP API Handlers
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		http.ServeFile(w, r, "index.html")
		return
	}
	if r.URL.Path == "/dashboard.js" {
		http.ServeFile(w, r, "dashboard.js")
		return
	}
	http.NotFound(w, r)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	whatsmeowMutex.Lock()
	defer whatsmeowMutex.Unlock()

	isConnected := false
	if client != nil {
		isConnected = client.IsConnected()
		if isConnected && connStatus != "CONNECTED" {
			connStatus = "CONNECTED"
			if client.Store.ID != nil {
				connectedPhone = client.Store.ID.User
			}
		} else if !isConnected && connStatus == "CONNECTED" {
			connStatus = "DISCONNECTED"
			connectedPhone = ""
		}
	}

	// Fetch active replies safely
	activeRepliesMutex.RLock()
	repliesList := make([]ActiveReply, 0, len(activeReplies))
	for _, r := range activeReplies {
		repliesList = append(repliesList, r)
	}
	activeRepliesMutex.RUnlock()

	statusMap := map[string]interface{}{
		"connection_status": connStatus,
		"qr_code":           qrCodeString,
		"connected_phone":   connectedPhone,
		"active_replies":    repliesList,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statusMap)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		configMutex.RLock()
		defer configMutex.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)
		return
	}

	if r.Method == http.MethodPost {
		var newCfg Config
		err := json.NewDecoder(r.Body).Decode(&newCfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		saveConfig(newCfg)
		addLog("INFO", "bot_action", "", "কনফিগারেশন আপডেট করা হয়েছে।")

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success"}`))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		data, err := os.ReadFile(logsPath)
		if err != nil {
			w.Write([]byte("[]"))
			return
		}
		w.Write(data)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func handleClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clearLogs()
	addLog("INFO", "bot_action", "", "লগ পরিষ্কার করা হয়েছে।")
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	whatsmeowMutex.Lock()
	defer whatsmeowMutex.Unlock()

	if client != nil {
		err := client.Logout(context.Background())
		if err != nil {
			addLog("ERROR", "connection_status", "", fmt.Sprintf("লগআউট করতে ব্যর্থ: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client.Disconnect()

		qrCodeString = ""
		connStatus = "DISCONNECTED"
		connectedPhone = ""

		addLog("INFO", "connection_status", "", "WhatsApp ডিভাইস লগআউট করা হয়েছে।")

		deviceStore, err := container.GetFirstDevice(context.Background())
		if err == nil {
			client = whatsmeow.NewClient(deviceStore, clientLog)
			client.AddEventHandler(eventHandler)
		}
	}

	go connectWhatsApp()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success"}`))
}

func main() {
	godotenv.Load()

	// Handle persistent data directory for Render.com hosting
	if envDir := os.Getenv("DATA_DIR"); envDir != "" {
		dataDir = envDir
		os.MkdirAll(dataDir, 0755)
		configPath = filepath.Join(dataDir, "config.json")
		logsPath = filepath.Join(dataDir, "logs.json")
		dbPath = fmt.Sprintf("file:%s?_foreign_keys=on", filepath.Join(dataDir, "store.db"))
	}

	loadConfig()

	if _, err := os.Stat(logsPath); os.IsNotExist(err) {
		os.WriteFile(logsPath, []byte("[]"), 0644)
	}

	dbLog := waLog.Stdout("Database", "ERROR", true)
	var err error
	container, err = sqlstore.New(context.Background(), "sqlite3", dbPath, dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		panic(err)
	}

	clientLog = waLog.Stdout("Client", "ERROR", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	// Run connection flow in background
	go connectWhatsApp()

	// Web server endpoints
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/dashboard.js", handleDashboard)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/logs", handleLogs)
	http.HandleFunc("/api/logs/clear", handleClearLogs)
	http.HandleFunc("/api/logout", handleLogout)

	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("I am awake!"))
	})

	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = defaultPort
		}
		fmt.Printf("Dashboard Web Server Active on port %s\n", port)
		http.ListenAndServe(":"+port, nil)
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}
