package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	reCode = regexp.MustCompile(`\b\d{4,8}\b`)
	reLink = regexp.MustCompile(`https?://[^\s<>"'\)]+`)
)

type AccountRecord struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

type BatchGenState struct {
	sync.Mutex
	IsRunning bool            `json:"is_running"`
	Target    int             `json:"target"`
	Done      int             `json:"done"`
	Successes []AccountRecord `json:"successes"`
	Errors    []string        `json:"errors"`
	cancel    context.CancelFunc
}

var (
	mu           sync.RWMutex
	accountCache = make(map[string]*AccountRecord)
	tokenCache   = make(map[string]string)
	credFilePath string
	batchState   BatchGenState
)

type DisplayMessage struct {
	ID      string   `json:"id"`
	From    string   `json:"from"`
	Subject string   `json:"subject"`
	Date    string   `json:"date"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
	OTP     string   `json:"otp"`
	Links   []string `json:"links"`
}

type MailboxResult struct {
	Email    string           `json:"email"`
	Messages []DisplayMessage `json:"messages"`
	LastOTP  string           `json:"last_otp"`
	LastLink string           `json:"last_link"`
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		num, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[num.Int64()]
	}
	return string(b)
}

func getMailTmDomain() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://api.mail.tm/domains")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data struct {
		Members []struct {
			Domain string `json:"domain"`
		} `json:"hydra:member"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if len(data.Members) == 0 {
		return "fldubai.com", nil
	}
	return data.Members[0].Domain, nil
}

func createMailTmAccount(email, password string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	body, _ := json.Marshal(map[string]string{
		"address":  strings.ToLower(email),
		"password": password,
	})
	resp, err := client.Post("https://api.mail.tm/accounts", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create account failed (%d): %s", resp.StatusCode, string(raw))
	}
	return nil
}

func getMailTmToken(email, password string) (string, error) {
	email = strings.ToLower(email)
	mu.RLock()
	if t, ok := tokenCache[email]; ok && t != "" {
		mu.RUnlock()
		return t, nil
	}
	mu.RUnlock()

	client := &http.Client{Timeout: 10 * time.Second}
	body, _ := json.Marshal(map[string]string{
		"address":  email,
		"password": password,
	})
	resp, err := client.Post("https://api.mail.tm/token", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Token == "" {
		return "", errors.New("empty token returned")
	}

	mu.Lock()
	tokenCache[email] = res.Token
	mu.Unlock()
	return res.Token, nil
}

func extractOTP(text string) string {
	matches := reCode.FindAllString(text, -1)
	for _, m := range matches {
		if len(m) >= 4 && len(m) <= 8 {
			return m
		}
	}
	return ""
}

func extractVerificationLinks(text string) []string {
	matches := reLink.FindAllString(text, -1)
	var links []string
	seen := make(map[string]bool)
	for _, l := range matches {
		l = strings.TrimRight(l, ".,;)\"'>")
		lower := strings.ToLower(l)
		if strings.Contains(lower, "verify") || strings.Contains(lower, "confirm") || strings.Contains(lower, "token") || strings.Contains(lower, "activate") || strings.Contains(lower, "auth") || strings.Contains(lower, "validation") {
			if !seen[l] {
				seen[l] = true
				links = append(links, l)
			}
		}
	}
	if len(links) == 0 {
		for _, l := range matches {
			l = strings.TrimRight(l, ".,;)\"'>")
			if !seen[l] && strings.HasPrefix(l, "http") {
				seen[l] = true
				links = append(links, l)
				if len(links) >= 3 {
					break
				}
			}
		}
	}
	return links
}

func fetchMailTmMessages(email, password, token string) ([]DisplayMessage, string, string, error) {
	email = strings.ToLower(email)
	if token == "" && password != "" {
		var err error
		token, err = getMailTmToken(email, password)
		if err != nil {
			return nil, "", "", err
		}
	}
	if token == "" {
		return nil, "", "", errors.New("no auth token or password found for account")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "https://api.mail.tm/messages?page=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		mu.Lock()
		delete(tokenCache, email)
		mu.Unlock()
		if password != "" {
			token, err = getMailTmToken(email, password)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err = client.Do(req)
				if err != nil {
					return nil, "", "", err
				}
				defer resp.Body.Close()
			}
		}
	}

	var data struct {
		Members []struct {
			ID   string `json:"id"`
			From struct {
				Address string `json:"address"`
				Name    string `json:"name"`
			} `json:"from"`
			Subject   string `json:"subject"`
			Intro     string `json:"intro"`
			CreatedAt string `json:"createdAt"`
		} `json:"hydra:member"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, "", "", err
	}

	var messages []DisplayMessage
	lastOTP := ""
	lastLink := ""

	for _, item := range data.Members {
		msg := DisplayMessage{
			ID:      item.ID,
			From:    item.From.Address,
			Subject: item.Subject,
			Date:    item.CreatedAt,
			Text:    item.Intro,
		}
		if item.From.Name != "" {
			msg.From = fmt.Sprintf("%s <%s>", item.From.Name, item.From.Address)
		}

		detailReq, _ := http.NewRequest(http.MethodGet, "https://api.mail.tm/messages/"+item.ID, nil)
		detailReq.Header.Set("Authorization", "Bearer "+token)
		dResp, dErr := client.Do(detailReq)
		if dErr == nil && dResp.StatusCode == http.StatusOK {
			var detail struct {
				Text string   `json:"text"`
				HTML []string `json:"html"`
			}
			_ = json.NewDecoder(dResp.Body).Decode(&detail)
			dResp.Body.Close()
			if detail.Text != "" {
				msg.Text = detail.Text
			}
			if len(detail.HTML) > 0 {
				msg.HTML = detail.HTML[0]
			}
		}

		contentForScan := msg.Subject + " " + msg.Text + " " + msg.HTML
		otp := extractOTP(contentForScan)
		msg.OTP = otp
		if lastOTP == "" && otp != "" {
			lastOTP = otp
		}

		links := extractVerificationLinks(contentForScan)
		msg.Links = links
		if lastLink == "" && len(links) > 0 {
			lastLink = links[0]
		}

		messages = append(messages, msg)
	}

	return messages, lastOTP, lastLink, nil
}

func fetchCatchmailMessages(email string) ([]DisplayMessage, string, string, error) {
	mailboxURL := fmt.Sprintf("https://api.catchmail.io/api/v1/mailbox?address=%s", url.QueryEscape(email))
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(mailboxURL)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	var mBox struct {
		Messages []struct {
			ID      string `json:"id"`
			From    string `json:"from"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&mBox)

	var list []DisplayMessage
	lastOTP := ""
	lastLink := ""

	for _, m := range mBox.Messages {
		detailURL := fmt.Sprintf("https://api.catchmail.io/api/v1/message/%s?mailbox=%s", url.PathEscape(m.ID), url.QueryEscape(email))
		dResp, err := client.Get(detailURL)
		bodyText := m.Subject
		bodyHTML := ""
		if err == nil {
			var detail struct {
				Body struct {
					Text string `json:"text"`
					HTML string `json:"html"`
				} `json:"body"`
			}
			_ = json.NewDecoder(dResp.Body).Decode(&detail)
			dResp.Body.Close()
			if detail.Body.Text != "" {
				bodyText = detail.Body.Text
			}
			if detail.Body.HTML != "" {
				bodyHTML = detail.Body.HTML
			}
		}

		content := m.Subject + " " + bodyText + " " + bodyHTML
		otp := extractOTP(content)
		links := extractVerificationLinks(content)
		if lastOTP == "" && otp != "" {
			lastOTP = otp
		}
		if lastLink == "" && len(links) > 0 {
			lastLink = links[0]
		}

		list = append(list, DisplayMessage{
			ID:      m.ID,
			From:    m.From,
			Subject: m.Subject,
			Text:    bodyText,
			HTML:    bodyHTML,
			OTP:     otp,
			Links:   links,
		})
	}
	return list, lastOTP, lastLink, nil
}

func loadCredentials(path string) {
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.Split(l, "|")
		authPart := strings.TrimSpace(parts[0])
		colonIdx := strings.Index(authPart, ":")
		if colonIdx == -1 {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(authPart[:colonIdx]))
		pass := strings.TrimSpace(authPart[colonIdx+1:])

		token := ""
		created := ""
		for _, p := range parts[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "Token:") {
				token = strings.TrimSpace(strings.TrimPrefix(p, "Token:"))
			} else if strings.HasPrefix(p, "RefreshToken:") {
				// RefreshToken
			} else if strings.HasPrefix(p, "Created:") {
				created = strings.TrimSpace(strings.TrimPrefix(p, "Created:"))
			}
		}

		rec := &AccountRecord{
			Email:     email,
			Password:  pass,
			Token:     token,
			CreatedAt: created,
		}
		accountCache[email] = rec
		if token != "" {
			tokenCache[email] = token
		}
	}
}

func saveNewAccountToFile(path string, rec *AccountRecord) error {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	line := fmt.Sprintf("%s:%s\n", rec.Email, rec.Password)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func startBatchGeneration(count int) {
	batchState.Lock()
	if batchState.IsRunning {
		batchState.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	batchState.cancel = cancel
	batchState.IsRunning = true
	batchState.Target = count
	batchState.Done = 0
	batchState.Successes = []AccountRecord{}
	batchState.Errors = []string{}
	batchState.Unlock()

	go func() {
		domain, err := getMailTmDomain()
		if err != nil || domain == "" {
			domain = "fldubai.com"
		}

		workers := 3
		if workers > count {
			workers = count
		}
		tasks := make(chan int, count)
		for i := 1; i <= count; i++ {
			tasks <- i
		}
		close(tasks)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(wID int) {
				defer wg.Done()
				for item := range tasks {
					select {
					case <-ctx.Done():
						return
					default:
					}

					randID := randomString(8)
					email := fmt.Sprintf("iii85iii_%s@%s", randID, domain)
					pass := fmt.Sprintf("III85III_%s@2026", randID)

					cErr := createMailTmAccount(email, pass)
					if cErr != nil {
						errMsg := fmt.Sprintf("[%s] خطا در ایجاد اکانت %d: %v", time.Now().Format("15:04:05"), item, cErr)
						batchState.Lock()
						batchState.Errors = append(batchState.Errors, errMsg)
						batchState.Done++
						batchState.Unlock()
						continue
					}

					token, _ := getMailTmToken(email, pass)
					rec := AccountRecord{
						Email:     email,
						Password:  pass,
						Token:     token,
						CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
					}

					mu.Lock()
					accountCache[email] = &rec
					mu.Unlock()

					_ = saveNewAccountToFile(credFilePath, &rec)

					batchState.Lock()
					batchState.Successes = append(batchState.Successes, rec)
					batchState.Done++
					batchState.Unlock()
				}
			}(w)
		}

		wg.Wait()
		batchState.Lock()
		batchState.IsRunning = false
		batchState.Unlock()
	}()
}

const uiTemplate = `<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>III85III — پنل وب ایمیل موقت و دریافت زنده OTP</title>
    <style>
        :root {
            --bg-color: #0b0f17;
            --surface: #141b26;
            --surface-hover: #1c2636;
            --border: #233044;
            --primary: #00d2ff;
            --primary-glow: rgba(0, 210, 255, 0.25);
            --success: #00e676;
            --error: #ff3366;
            --text-main: #f0f6fc;
            --text-muted: #8b9bb4;
            --accent: #7928ca;
        }
        * { box-sizing: border-box; margin: 0; padding: 0; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
        body { background: var(--bg-color); color: var(--text-main); min-height: 100vh; display: flex; flex-direction: column; }
        header { background: var(--surface); border-bottom: 1px solid var(--border); padding: 14px 24px; display: flex; justify-content: space-between; align-items: center; position: sticky; top: 0; z-index: 100; box-shadow: 0 4px 20px rgba(0,0,0,0.4); }
        .logo-area { display: flex; align-items: center; gap: 12px; }
        .logo-badge { background: linear-gradient(135deg, #00d2ff, #7928ca); color: #fff; font-weight: 900; font-size: 14px; padding: 6px 12px; border-radius: 8px; letter-spacing: 1px; }
        .logo-title { font-size: 18px; font-weight: 700; color: #fff; }
        
        .nav-tabs { display: flex; gap: 8px; }
        .tab-btn { background: #1c2636; color: var(--text-muted); border: 1px solid var(--border); padding: 8px 16px; border-radius: 8px; font-weight: 600; font-size: 13px; cursor: pointer; transition: 0.2s; }
        .tab-btn.active { background: var(--primary); color: #000; font-weight: 800; border-color: var(--primary); }
        
        .app-layout { display: flex; flex: 1; max-width: 1400px; margin: 0 auto; width: 100%; padding: 20px; gap: 20px; }
        
        .sidebar { width: 340px; background: var(--surface); border: 1px solid var(--border); border-radius: 12px; display: flex; flex-direction: column; height: calc(100vh - 120px); position: sticky; top: 80px; }
        .sidebar-header { padding: 16px; border-bottom: 1px solid var(--border); }
        .btn-new-email { width: 100%; background: linear-gradient(135deg, #00d2ff, #0070f3); color: #fff; border: none; padding: 12px; border-radius: 8px; font-weight: 700; font-size: 15px; cursor: pointer; display: flex; align-items: center; justify-content: center; gap: 8px; transition: 0.2s; box-shadow: 0 0 15px var(--primary-glow); }
        .btn-new-email:hover { transform: translateY(-1px); filter: brightness(1.1); }
        .accounts-list { overflow-y: auto; flex: 1; padding: 10px; }
        .account-card { background: var(--surface-hover); border: 1px solid var(--border); border-radius: 8px; padding: 12px; margin-bottom: 8px; cursor: pointer; transition: 0.2s; direction: ltr; text-align: left; }
        .account-card:hover { border-color: var(--primary); background: #192231; }
        .account-card.active { border-color: var(--primary); background: rgba(0, 210, 255, 0.08); box-shadow: 0 0 10px var(--primary-glow); }
        .account-email { font-size: 13px; font-weight: 600; color: #fff; word-break: break-all; }
        .account-date { font-size: 11px; color: var(--text-muted); margin-top: 4px; }

        .workspace { flex: 1; display: flex; flex-direction: column; gap: 16px; }

        /* Batch Generator Card */
        .generator-card { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 20px; }
        .gen-controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 16px; }
        .input-count { background: #0b0f17; border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; color: #fff; font-size: 15px; width: 100px; text-align: center; }
        .btn-start { background: linear-gradient(135deg, #00e676, #00b0ff); color: #000; border: none; padding: 10px 22px; border-radius: 8px; font-weight: 800; font-size: 14px; cursor: pointer; transition: 0.2s; }
        .btn-stop { background: #ff3366; color: #fff; border: none; padding: 10px 20px; border-radius: 8px; font-weight: 700; font-size: 14px; cursor: pointer; }
        
        .progress-bar-bg { width: 100%; height: 10px; background: #0b0f17; border-radius: 5px; overflow: hidden; margin-bottom: 16px; }
        .progress-bar-fill { height: 100%; width: 0%; background: linear-gradient(90deg, #00d2ff, #00e676); transition: width 0.3s; }

        .results-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
        .box-success { background: rgba(0, 230, 118, 0.05); border: 1px solid rgba(0, 230, 118, 0.3); border-radius: 10px; padding: 14px; max-height: 280px; overflow-y: auto; }
        .box-error { background: rgba(255, 51, 102, 0.05); border: 1px solid rgba(255, 51, 102, 0.3); border-radius: 10px; padding: 14px; max-height: 280px; overflow-y: auto; }
        .box-title { font-size: 14px; font-weight: 700; margin-bottom: 10px; display: flex; justify-content: space-between; align-items: center; }
        .title-success { color: var(--success); }
        .title-error { color: var(--error); }
        .item-card { background: #0b0f17; border: 1px solid var(--border); border-radius: 6px; padding: 8px 12px; margin-bottom: 8px; font-size: 12px; direction: ltr; text-align: left; display: flex; justify-content: space-between; align-items: center; }
        .err-item { background: #0b0f17; border-left: 3px solid var(--error); padding: 8px 12px; margin-bottom: 6px; font-size: 12px; color: #ff85a2; direction: ltr; text-align: left; }

        /* Active Account Banner */
        .active-account-banner { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 20px; display: flex; flex-direction: column; gap: 14px; }
        .banner-row { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; }
        .email-display { font-size: 20px; font-weight: 800; color: var(--primary); direction: ltr; font-family: monospace; }
        .actions-group { display: flex; gap: 8px; }
        .btn-copy { background: #233044; color: #fff; border: 1px solid #334460; padding: 8px 14px; border-radius: 6px; font-size: 13px; font-weight: 600; cursor: pointer; transition: 0.2s; }
        .btn-copy:hover { background: #2c3e59; border-color: var(--primary); }

        .otp-banner { background: linear-gradient(135deg, rgba(0, 210, 255, 0.15), rgba(121, 40, 202, 0.15)); border: 2px solid var(--primary); border-radius: 12px; padding: 20px; text-align: center; box-shadow: 0 0 25px var(--primary-glow); display: none; }
        .otp-number { font-size: 38px; font-weight: 900; letter-spacing: 8px; color: #00d2ff; font-family: monospace; cursor: pointer; margin: 6px 0; }
        .link-banner { background: rgba(0, 230, 118, 0.1); border: 2px solid var(--success); border-radius: 12px; padding: 16px 20px; display: flex; align-items: center; justify-content: space-between; gap: 14px; display: none; }
        .btn-open-link { background: var(--success); color: #000; font-weight: 800; font-size: 13px; padding: 10px 18px; border-radius: 8px; text-decoration: none; }

        .messages-container { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 20px; min-height: 250px; }
        .msg-card { background: var(--surface-hover); border: 1px solid var(--border); border-radius: 10px; padding: 16px; margin-bottom: 10px; }
        .msg-content { background: var(--bg-color); border: 1px solid var(--border); border-radius: 8px; padding: 12px; font-size: 13px; line-height: 1.6; max-height: 200px; overflow-y: auto; direction: ltr; text-align: left; white-space: pre-wrap; color: #cbd5e1; }
        
        .pulse-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--success); animation: pulse 1.5s infinite; }
        @keyframes pulse { 0% { opacity: 1; } 50% { opacity: 0.3; } 100% { opacity: 1; } }
    </style>
</head>
<body>
    <header>
        <div class="logo-area">
            <span class="logo-badge">III85III</span>
            <span class="logo-title">Pool-Gen & OTP Live Hub</span>
        </div>
        <div style="display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--success);">
            <span class="pulse-dot"></span>
            <span>مانیتورینگ زنده فعال است</span>
        </div>
    </header>

    <div class="app-layout">
        <!-- Sidebar -->
        <aside class="sidebar">
            <div class="sidebar-header">
                <button class="btn-new-email" onclick="createNewEmail()">
                    <span>✨</span>
                    <span>ساخت فوری ۱ ایمیل موقت</span>
                </button>
                <button class="btn-copy" style="width: 100%; margin-top: 8px; justify-content: center; background: #1c2636;" onclick="copyAllAccounts()">
                    <span>📋 کپی همه اکانت‌ها (email:pass)</span>
                </button>
                <input type="text" id="sidebarSearch" style="width: 100%; margin-top: 8px; background: #0b0f17; border: 1px solid var(--border); border-radius: 6px; padding: 6px 10px; color: #fff; font-size: 12px; direction: ltr;" placeholder="جستجو در اکانت‌ها..." oninput="filterAccounts(this.value)">
            </div>
            <div class="accounts-list" id="accountsList"></div>
        </aside>

        <!-- Main Workspace -->
        <main class="workspace">
            <!-- Batch Generator Panel with Success / Error -->
            <div class="generator-card">
                <div style="font-size: 16px; font-weight: 700; margin-bottom: 12px;">⚡ ساخت دسته‌ای اکانت‌ها (بدون نیاز به ترمینال)</div>
                <div class="gen-controls">
                    <label style="font-size: 13px; color: var(--text-muted);">تعداد ساخت:</label>
                    <input type="number" id="batchCountInput" class="input-count" value="5" min="1" max="100">
                    <button class="btn-start" id="btnStartBatch" onclick="startBatch()">🚀 شروع ساخت دسته‌ای</button>
                    <button class="btn-stop" id="btnStopBatch" onclick="stopBatch()" style="display: none;">⏹ توقف</button>
                    <span id="genStatusText" style="font-size: 13px; color: var(--text-muted); margin-right: 10px;">آماده به کار</span>
                </div>

                <div class="progress-bar-bg">
                    <div class="progress-bar-fill" id="progressBarFill"></div>
                </div>

                <!-- Real-time Success and Error sections -->
                <div class="results-grid">
                    <div class="box-success">
                        <div class="box-title title-success">
                            <span>🟢 اکانت‌های موفق (Successes)</span>
                            <span id="successCountBadge">۰</span>
                        </div>
                        <div id="successListContainer">
                            <div style="text-align: center; color: var(--text-muted); font-size: 12px; padding: 20px;">هنوز اکانتی ساخته نشده است.</div>
                        </div>
                    </div>

                    <div class="box-error">
                        <div class="box-title title-error">
                            <span>🔴 خطاها و هشدارها (Errors)</span>
                            <span id="errorCountBadge">۰</span>
                        </div>
                        <div id="errorListContainer">
                            <div style="text-align: center; color: var(--text-muted); font-size: 12px; padding: 20px;">هیچ خطایی وجود ندارد. همه چیز عالی است!</div>
                        </div>
                    </div>
                </div>
            </div>

            <!-- Active Account Banner & Input Box -->
            <div class="active-account-banner">
                <div style="font-size: 14px; font-weight: 800; color: #fff; display: flex; align-items: center; gap: 8px;">
                    <span>📬 مشاهده اینباکس و دریافت کد برای هر ایمیل:</span>
                </div>
                <div style="display: flex; gap: 10px; flex-wrap: wrap;">
                    <input type="text" id="manualEmailInput" style="flex: 1; min-width: 280px; background: #0b0f17; border: 2px solid var(--primary); border-radius: 8px; padding: 12px 16px; color: #fff; font-size: 15px; direction: ltr; font-family: monospace; font-weight: 700;" placeholder="آدرس ایمیل مورد نظر را اینجا وارد یا پیست کنید..." onkeydown="if(event.key==='Enter') applyManualEmail()">
                    <button class="btn-start" style="padding: 12px 22px; font-size: 14px;" onclick="applyManualEmail()">🔍 باز کردن اینباکس و دریافت کد</button>
                </div>
                <div class="banner-row" style="margin-top: 4px; padding-top: 12px; border-top: 1px solid var(--border);">
                    <div>
                        <div style="font-size: 11px; color: var(--text-muted); margin-bottom: 2px;">ایمیل در حال بررسی:</div>
                        <div class="email-display" id="activeEmailDisplay">در حال بارگذاری...</div>
                    </div>
                    <div class="actions-group">
                        <button class="btn-copy" onclick="copyActiveEmail()">📋 کپی ایمیل</button>
                        <button class="btn-copy" onclick="copyActivePassword()">🔑 کپی رمز عبور</button>
                    </div>
                </div>
                <div style="font-size: 12px; color: var(--text-muted);">
                    رمز عبور: <code id="activePassDisplay" style="color: var(--primary);">-</code>
                </div>
            </div>

            <!-- OTP Highlight Card -->
            <div class="otp-banner" id="otpBanner">
                <div style="font-size: 14px; color: #a0aec0; margin-bottom: 6px;">کد تایید هویت شناسایی‌شده (OTP):</div>
                <div class="otp-number" id="otpValue" onclick="copyOTP()">------</div>
                <div style="font-size: 12px; color: var(--text-muted);">(برای کپی روی کد کلیک کنید)</div>
            </div>

            <!-- Verification Link Banner -->
            <div class="link-banner" id="linkBanner">
                <div>
                    <div style="font-size: 14px; font-weight: 700;">🔗 لینک تایید و فعال‌سازی پیدا شد:</div>
                    <div style="font-size: 11px; color: var(--text-muted); max-width: 450px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: ltr;" id="linkUrlDisplay"></div>
                </div>
                <a href="#" target="_blank" class="btn-open-link" id="btnOpenLink">تایید ایمیل و فعال‌سازی حساب 🚀</a>
            </div>

            <!-- Messages Area -->
            <div class="messages-container">
                <div style="font-size: 15px; font-weight: 700; margin-bottom: 12px; display: flex; justify-content: space-between;">
                    <span>صندوق ورودی پیام‌ها (Live Inbox)</span>
                    <span id="msgCountBadge" style="font-size: 12px; color: var(--text-muted);">۰ پیام</span>
                </div>
                <div id="messagesArea">
                    <div style="text-align: center; color: var(--text-muted); padding: 30px; font-size: 13px;">
                        ایمیل را در گیت‌هاب یا هر سایت دیگری وارد کنید.<br>پیام‌ها و کدهای تایید به صورت زنده در اینجا نمایش داده می‌شوند.
                    </div>
                </div>
            </div>
        </main>
    </div>

    <script>
        var currentEmail = "";
        var currentPassword = "";
        var pollTimer = null;
        var batchPollTimer = null;
        var accounts = [];

        function init() {
            loadAccounts().then(function() {
                if (accounts.length > 0) {
                    selectEmail(accounts[0].email, accounts[0].password);
                } else {
                    createNewEmail();
                }
            });
            batchPollTimer = setInterval(pollBatchStatus, 1500);
        }

        function loadAccounts() {
            return fetch('/api/accounts')
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    accounts = data || [];
                    renderAccountsList();
                })
                .catch(function(e) { console.error(e); });
        }

        function copyText(val, label) {
            if (!val) return;
            navigator.clipboard.writeText(val);
            alert(label + ' کپی شد:\n' + val);
        }

        function copyAllAccounts() {
            if (!accounts || accounts.length === 0) {
                alert('هیچ اکانتی برای کپی وجود ندارد.');
                return;
            }
            var lines = accounts.map(function(a) { return a.email + ':' + a.password; }).join('\n');
            navigator.clipboard.writeText(lines);
            alert('تعداد ' + accounts.length + ' اکانت به صورت تمیز کپی شد:\n\n' + lines.substring(0, 150) + '...\n\n(تماماً در کلیپ‌بورد کپی شد)');
        }

        function applyManualEmail(val) {
            var input = document.getElementById('manualEmailInput');
            var emailVal = (val !== undefined ? val : (input ? input.value : "")).trim();
            if (!emailVal) {
                alert('لطفاً آدرس ایمیل را وارد کنید.');
                return;
            }
            var pass = "";
            var m = emailVal.match(/iii85iii_([a-z0-9]+)@/i);
            if (m) {
                pass = "III85III_" + m[1] + "@2026";
            }
            selectEmail(emailVal, pass);
        }

        function filterAccounts(query) {
            query = (query || "").toLowerCase().trim();
            if (!accounts || accounts.length === 0) return;
            var filtered = accounts.filter(function(a) {
                return a.email.toLowerCase().indexOf(query) !== -1;
            });
            renderAccountsList(filtered);
        }

        function renderAccountsList(customList) {
            var list = customList !== undefined ? customList : accounts;
            var listEl = document.getElementById('accountsList');
            if (!list || list.length === 0) {
                listEl.innerHTML = '<div style="text-align: center; color: var(--text-muted); font-size: 12px; padding: 20px;">موردی یافت نشد.</div>';
                return;
            }
            var html = '';
            for (var i = 0; i < list.length; i++) {
                var acc = list[i];
                var isActive = acc.email.toLowerCase() === currentEmail.toLowerCase();
                html += '<div class="account-card ' + (isActive ? 'active' : '') + '">';
                html += '<div style="cursor: pointer;" onclick="selectEmail(\'' + acc.email + '\', \'' + (acc.password || '') + '\')">';
                html += '<div class="account-email">' + (i + 1) + '. ' + acc.email + '</div>';
                html += '<div style="font-size: 11px; color: var(--primary); margin-top: 3px;">رمز: ' + (acc.password || '') + '</div>';
                html += '</div>';
                html += '<div style="display: flex; gap: 6px; margin-top: 6px;">';
                html += '<button class="btn-copy" style="padding: 2px 8px; font-size: 11px; flex: 1;" onclick="copyText(\'' + acc.email + '\', \'ایمیل\')">📋 کپی ایمیل</button>';
                html += '<button class="btn-copy" style="padding: 2px 8px; font-size: 11px; flex: 1;" onclick="copyText(\'' + acc.password + '\', \'رمز عبور\')">🔑 کپی پسورد</button>';
                html += '</div>';
                html += '</div>';
            }
            listEl.innerHTML = html;
        }

        function selectEmail(email, password) {
            if (!email) return;
            currentEmail = email.toLowerCase().trim();
            for (var i = 0; i < accounts.length; i++) {
                if (accounts[i].email.toLowerCase() === currentEmail) {
                    password = accounts[i].password;
                    break;
                }
            }
            if (!password) {
                var m = currentEmail.match(/iii85iii_([a-z0-9]+)@/i);
                if (m) {
                    password = "III85III_" + m[1] + "@2026";
                }
            }
            currentPassword = password || "";

            document.getElementById('activeEmailDisplay').innerText = currentEmail;
            document.getElementById('activePassDisplay').innerText = currentPassword || '(خودکار/بدون رمز)';
            var inp = document.getElementById('manualEmailInput');
            if (inp) inp.value = currentEmail;

            renderAccountsList();
            checkMessages();

            if (pollTimer) clearInterval(pollTimer);
            pollTimer = setInterval(checkMessages, 3500);
        }

        function createNewEmail() {
            fetch('/api/new-email', { method: 'POST' })
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    if (data.success) {
                        loadAccounts().then(function() {
                            selectEmail(data.email, data.password);
                        });
                    }
                });
        }

        function startBatch() {
            var count = parseInt(document.getElementById('batchCountInput').value) || 5;
            fetch('/api/start-batch?count=' + count, { method: 'POST' })
                .then(function(res) { return res.json(); })
                .then(function() {
                    pollBatchStatus();
                });
        }

        function stopBatch() {
            fetch('/api/stop-batch', { method: 'POST' });
        }

        function pollBatchStatus() {
            fetch('/api/batch-status')
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    var btnStart = document.getElementById('btnStartBatch');
                    var btnStop = document.getElementById('btnStopBatch');
                    var statusText = document.getElementById('genStatusText');
                    var fill = document.getElementById('progressBarFill');

                    if (data.is_running) {
                        btnStart.style.display = 'none';
                        btnStop.style.display = 'inline-block';
                        var pct = data.target > 0 ? Math.round((data.done / data.target) * 100) : 0;
                        fill.style.width = pct + '%';
                        statusText.innerText = 'در حال ساخت... (' + data.done + ' از ' + data.target + ') ' + pct + '%';
                    } else {
                        btnStart.style.display = 'inline-block';
                        btnStop.style.display = 'none';
                        if (data.target > 0 && data.done >= data.target) {
                            fill.style.width = '100%';
                            statusText.innerText = 'تکمیل شد (' + data.done + ' اکانت)';
                            loadAccounts();
                        }
                    }

                    // Render Successes
                    document.getElementById('successCountBadge').innerText = (data.successes ? data.successes.length : 0);
                    var sContainer = document.getElementById('successListContainer');
                    if (data.successes && data.successes.length > 0) {
                        var sHtml = '';
                        for (var i = data.successes.length - 1; i >= 0; i--) {
                            var s = data.successes[i];
                            sHtml += '<div class="item-card" style="flex-direction: column; gap: 4px;">';
                            sHtml += '<div style="display: flex; justify-content: space-between; align-items: center; width: 100%;">';
                            sHtml += '<strong style="color: #fff; font-size: 13px;">' + s.email + '</strong>';
                            sHtml += '<button class="btn-copy" style="padding: 2px 8px; font-size: 11px;" onclick="selectEmail(\'' + s.email + '\', \'' + s.password + '\')">اینباکس</button>';
                            sHtml += '</div>';
                            sHtml += '<div style="display: flex; justify-content: space-between; align-items: center; width: 100%; color: var(--primary); font-size: 11px;">';
                            sHtml += '<span>رمز: ' + s.password + '</span>';
                            sHtml += '<button class="btn-copy" style="padding: 2px 6px; font-size: 10px;" onclick="copyText(\'' + s.email + ':' + s.password + '\', \'اکانت\')">📋 کپی</button>';
                            sHtml += '</div>';
                            sHtml += '</div>';
                        }
                        sContainer.innerHTML = sHtml;
                    }

                    // Render Errors
                    document.getElementById('errorCountBadge').innerText = (data.errors ? data.errors.length : 0);
                    var eContainer = document.getElementById('errorListContainer');
                    if (data.errors && data.errors.length > 0) {
                        var eHtml = '';
                        for (var j = data.errors.length - 1; j >= 0; j--) {
                            eHtml += '<div class="err-item">' + data.errors[j] + '</div>';
                        }
                        eContainer.innerHTML = eHtml;
                    }
                });
        }

        function checkMessages() {
            if (!currentEmail) return;

            fetch('/api/messages?email=' + encodeURIComponent(currentEmail) + '&password=' + encodeURIComponent(currentPassword))
                .then(function(res) { return res.json(); })
                .then(function(data) {
                    var otpBanner = document.getElementById('otpBanner');
                    if (data.last_otp) {
                        otpBanner.style.display = 'block';
                        document.getElementById('otpValue').innerText = data.last_otp;
                    } else {
                        otpBanner.style.display = 'none';
                    }

                    var linkBanner = document.getElementById('linkBanner');
                    if (data.last_link) {
                        linkBanner.style.display = 'flex';
                        document.getElementById('linkUrlDisplay').innerText = data.last_link;
                        document.getElementById('btnOpenLink').href = data.last_link;
                    } else {
                        linkBanner.style.display = 'none';
                    }

                    var countBadge = document.getElementById('msgCountBadge');
                    var area = document.getElementById('messagesArea');

                    if (data.messages && data.messages.length > 0) {
                        countBadge.innerText = data.messages.length + ' پیام';
                        var html = '';
                        for (var i = 0; i < data.messages.length; i++) {
                            var m = data.messages[i];
                            html += '<div class="msg-card">';
                            html += '<div style="display: flex; justify-content: space-between; font-size: 12px; color: var(--text-muted); margin-bottom: 6px;"><span>' + m.from + '</span><span>' + (m.date || '') + '</span></div>';
                            html += '<div style="font-size: 15px; font-weight: 700; margin-bottom: 8px;">' + m.subject + '</div>';
                            if (m.otp) {
                                html += '<div style="margin-bottom: 8px;"><span style="background: rgba(0,210,255,0.15); border: 1px solid var(--primary); color: #00d2ff; padding: 4px 10px; border-radius: 6px; font-weight: 700; font-size: 13px;">کد OTP: ' + m.otp + '</span></div>';
                            }
                            if (m.links && m.links.length > 0) {
                                html += '<div style="margin-bottom: 8px;">';
                                for (var j = 0; j < m.links.length; j++) {
                                    html += '<a href="' + m.links[j] + '" target="_blank" class="btn-copy" style="color: var(--success); border-color: var(--success); text-decoration: none; margin-left: 6px; display: inline-block;">🔗 لینک فعال‌سازی</a>';
                                }
                                html += '</div>';
                            }
                            html += '<div class="msg-content">' + escapeHtml(m.text) + '</div>';
                            html += '</div>';
                        }
                        area.innerHTML = html;
                    } else {
                        countBadge.innerText = '۰ پیام';
                        area.innerHTML = '<div style="text-align: center; color: var(--text-muted); padding: 30px; font-size: 13px;">ایمیل را در گیت‌هاب یا هر سایت دیگری وارد کنید.<br>پیام‌ها و کدهای تایید به صورت زنده در اینجا نمایش داده می‌شوند.</div>';
                    }
                });
        }

        function copyActiveEmail() {
            if (!currentEmail) return;
            navigator.clipboard.writeText(currentEmail);
            alert('ایمیل کپی شد:\n' + currentEmail);
        }

        function copyActivePassword() {
            if (!currentPassword) return;
            navigator.clipboard.writeText(currentPassword);
            alert('رمز عبور کپی شد:\n' + currentPassword);
        }

        function copyOTP() {
            var val = document.getElementById('otpValue').innerText;
            if (val && val !== '------') {
                navigator.clipboard.writeText(val);
                alert('کد تایید کپی شد:\n' + val);
            }
        }

        function escapeHtml(text) {
            if (!text) return "";
            return text
                .replace(/&/g, "&amp;")
                .replace(/</g, "&lt;")
                .replace(/>/g, "&gt;")
                .replace(/"/g, "&quot;")
                .replace(/'/g, "&#039;");
        }

        window.onload = init;
    </script>
</body>
</html>
`

func main() {
	port := flag.Int("port", 8585, "Port to listen on")
	credFile := flag.String("creds", "accounts_credentials.txt", "Path to accounts credentials file")
	flag.Parse()

	credFilePath = *credFile
	if _, err := os.Stat(credFilePath); os.IsNotExist(err) {
		_ = os.WriteFile(credFilePath, []byte(""), 0600)
	}

	loadCredentials(credFilePath)
	log.Printf("Loaded credentials from %s (accounts: %d)", credFilePath, len(accountCache))

	tmpl := template.Must(template.New("ui").Parse(uiTemplate))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = tmpl.Execute(w, nil)
	})

	http.HandleFunc("/api/accounts", func(w http.ResponseWriter, r *http.Request) {
		loadCredentials(credFilePath)
		mu.RLock()
		var list []*AccountRecord
		for _, acc := range accountCache {
			list = append(list, acc)
		}
		mu.RUnlock()

		sort.Slice(list, func(i, j int) bool {
			return list[i].CreatedAt > list[j].CreatedAt
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	})

	http.HandleFunc("/api/new-email", func(w http.ResponseWriter, r *http.Request) {
		domain, err := getMailTmDomain()
		if err != nil {
			domain = "fldubai.com"
		}
		randID := randomString(8)
		email := fmt.Sprintf("iii85iii_%s@%s", randID, domain)
		pass := fmt.Sprintf("III85III_%s@2026", randID)

		if err := createMailTmAccount(email, pass); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": err.Error()})
			return
		}

		token, _ := getMailTmToken(email, pass)
		rec := &AccountRecord{
			Email:     email,
			Password:  pass,
			Token:     token,
			CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		}

		mu.Lock()
		accountCache[email] = rec
		mu.Unlock()

		_ = saveNewAccountToFile(credFilePath, rec)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    true,
			"email":      email,
			"password":   pass,
			"token":      token,
			"created_at": rec.CreatedAt,
		})
	})

	http.HandleFunc("/api/start-batch", func(w http.ResponseWriter, r *http.Request) {
		countStr := r.URL.Query().Get("count")
		c, err := strconv.Atoi(countStr)
		if err != nil || c <= 0 {
			c = 5
		}
		startBatchGeneration(c)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})

	http.HandleFunc("/api/stop-batch", func(w http.ResponseWriter, r *http.Request) {
		batchState.Lock()
		if batchState.cancel != nil {
			batchState.cancel()
		}
		batchState.IsRunning = false
		batchState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	})

	http.HandleFunc("/api/batch-status", func(w http.ResponseWriter, r *http.Request) {
		batchState.Lock()
		defer batchState.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(batchState)
	})

	http.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
		password := strings.TrimSpace(r.URL.Query().Get("password"))

		mu.RLock()
		rec := accountCache[email]
		mu.RUnlock()

		token := ""
		if rec != nil {
			if password == "" {
				password = rec.Password
			}
			token = rec.Token
		}

		var msgs []DisplayMessage
		var lastOTP, lastLink string
		var err error

		if strings.HasSuffix(email, "@catchmail.io") {
			msgs, lastOTP, lastLink, err = fetchCatchmailMessages(email)
		} else {
			msgs, lastOTP, lastLink, err = fetchMailTmMessages(email, password, token)
		}

		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			_ = json.NewEncoder(w).Encode(MailboxResult{
				Email:    email,
				Messages: []DisplayMessage{},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(MailboxResult{
			Email:    email,
			Messages: msgs,
			LastOTP:  lastOTP,
			LastLink: lastLink,
		})
	})

	serverURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
	log.Printf("III85III OTP Receiver Hub started at: %s", serverURL)

	go func() {
		time.Sleep(600 * time.Millisecond)
		openBrowser(serverURL)
	}()

	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log.Fatalf("Server exited: %v", err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
