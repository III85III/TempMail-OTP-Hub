# ⚡ TempMail-OTP-Hub

> **High-speed automated disposable email generator with a modern dark-mode web dashboard, real-time inbox polling, 1-click OTP code & verification link extraction, and high-concurrency batch account generation.**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Zero Dependencies](https://img.shields.io/badge/Dependencies-Zero%20(Pure%20Stdlib)-brightgreen?style=flat-square)](#)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20Linux%20%7C%20macOS-blue?style=flat-square)](#)

---

## 🌟 Highlights

- **⚡ Instant 1-Click Disposable Mail:** Generate fresh, functional temporary email inboxes with one click.
- **🔍 Real-Time Live OTP & Link Extractor:** Automatic regex engine detects 4-to-8 digit verification codes and activation links directly from incoming emails.
- **🖥️ Modern Dark-Mode Web Dashboard:** Cyberpunk dark UI served on `http://127.0.0.1:8585` with zero dependencies (pure Go standard library).
- **🚀 High-Concurrency Batch Generator:** Built-in worker pool to generate dozens of temporary accounts in parallel with live success/error streaming.
- **📋 Clean Credential Format:** Export and copy accounts instantly in standard `email:password` format.
- **🌐 Any Email Inspection:** Paste any previously created disposable email into the search bar to inspect its live inbox and historical messages.
- **🛡️ 100% Privacy-First & Self-Hosted:** No telemetry, no external trackers, runs entirely on your local machine.

---

## 🚀 Quick Start

### 1. Requirements
- [Go 1.22+](https://golang.org/dl/) installed (for compiling from source)
- Works on Windows, Linux, and macOS.

### 2. Run Directly
```bash
git clone https://github.com/III85III/TempMail-OTP-Hub.git
cd TempMail-OTP-Hub
go run main.go
```
The application will automatically launch your default browser to:
👉 **`http://127.0.0.1:8585`**

### 3. Build Binary
```bash
# Windows
go build -o tempmail-hub.exe main.go

# Linux / macOS
go build -o tempmail-hub main.go
./tempmail-hub -port 8585
```

---

## 🖥️ Web Dashboard Features

| Feature | Description |
| :--- | :--- |
| **Instant Generator** | Click `ساخت فوری ۱ ایمیل موقت` to generate a fresh email address in under 1 second. |
| **Batch Generator Panel** | Enter target count (e.g. `20`) and click `شروع ساخت دسته‌ای` to run concurrent workers. |
| **🟢 Success Box** | Displays freshly minted active credentials with instant one-click copy buttons. |
| **🔴 Error Box** | Shows real-time network or rate-limit warnings with automatic retry logic. |
| **Live OTP Banner** | Big highlighted verification code displayed with one-click clipboard copy. |
| **Activation Links** | One-click button to open email confirmation links in your browser. |

---

## ⚙️ CLI Options

```text
Usage of tempmail-hub:
  -port int
        Port to listen on (default 8585)
  -creds string
        Path to accounts credentials file (default "accounts_credentials.txt")
```

Example:
```bash
go run main.go -port 9090 -creds my_accounts.txt
```

---

## 📡 REST API Reference

| Endpoint | Method | Description |
| :--- | :--- | :--- |
| `/api/accounts` | `GET` | List all cached local accounts (`email:password`). |
| `/api/create-account` | `POST` | Generate one new disposable email address immediately. |
| `/api/mailbox?email=...` | `GET` | Fetch live inbox messages for the specified email. |
| `/api/batch-status` | `GET` | Query running batch progress (done, target, successes, errors). |
| `/api/start-batch?count=N` | `POST` | Trigger concurrent batch generation of `N` accounts. |
| `/api/stop-batch` | `POST` | Cancel ongoing batch generation. |

---

## 🛡️ Security & Privacy Notice

- This tool is designed for testing, development, QA registration flows, and temporary account management.
- **Never commit `accounts_credentials.txt`** to public version control. It is protected by `.gitignore` by default.
- Zero third-party dependencies are required — the entire server, static UI, HTTP router, and mail client are built using Go's standard library (`net/http`, `html/template`, `crypto/rand`).

---

## 📄 License

Distributed under the [MIT License](LICENSE).
Built with passion by [III85III](https://github.com/III85III).
