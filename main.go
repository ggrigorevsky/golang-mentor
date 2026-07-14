// Сервер сайта golang-mentor: раздаёт статику и принимает заявки из формы
// (JSON), отправляя их письмом через SMTP Яндекса.
//
// Запуск:
//
//	export SMTP_USER="you@yandex.ru"        // ваш ящик на Яндексе
//	export SMTP_PASS="app-password"         // пароль приложения: https://id.yandex.ru/security/app-passwords
//	export MAIL_TO="you@yandex.ru"          // куда слать заявки (по умолчанию = SMTP_USER)
//	go run .
//
// В Яндекс.Почте должен быть включён доступ по SMTP:
// Настройки → «Почтовые программы» → разрешить доступ с паролями приложений.
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"
)

type config struct {
	Addr      string // адрес HTTP-сервера, например :8080
	SMTPHost  string
	SMTPPort  string
	SMTPUser  string
	SMTPPass  string
	MailTo    string
	StaticDir string
}

func loadConfig() (config, error) {
	cfg := config{
		Addr:      envOr("ADDR", ":8080"),
		SMTPHost:  envOr("SMTP_HOST", "smtp.yandex.ru"),
		SMTPPort:  envOr("SMTP_PORT", "465"),
		SMTPUser:  os.Getenv("SMTP_USER"),
		SMTPPass:  os.Getenv("SMTP_PASS"),
		StaticDir: envOr("STATIC_DIR", "."),
	}
	cfg.MailTo = envOr("MAIL_TO", cfg.SMTPUser)
	if cfg.SMTPUser == "" || cfg.SMTPPass == "" {
		return cfg, fmt.Errorf("не заданы SMTP_USER и/или SMTP_PASS")
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/signup", signupHandler(cfg))
	mux.Handle("/", staticHandler(cfg.StaticDir))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("сервер запущен на %s, заявки уходят на %s", cfg.Addr, cfg.MailTo)
	log.Fatal(srv.ListenAndServe())
}

// staticHandler раздаёт файлы сайта, пряча служебные файлы бэкенда.
func staticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.ToLower(r.URL.Path)
		if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "go.mod") || strings.HasSuffix(p, "go.sum") {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// limiter — простая защита от спама: не чаще одной заявки в 10 секунд с одного IP.
var limiter = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func allow(ip string) bool {
	limiter.Lock()
	defer limiter.Unlock()
	now := time.Now()
	if t, ok := limiter.last[ip]; ok && now.Sub(t) < 10*time.Second {
		return false
	}
	limiter.last[ip] = now
	return true
}

// signupRequest — тело JSON-запроса от форм сайта.
// name и contact обязательны, остальные поля опциональны.
type signupRequest struct {
	Name    string `json:"name"`
	Contact string `json:"contact"`
	Course  string `json:"course,omitempty"`
	Dates   string `json:"dates,omitempty"`
	Level   string `json:"level,omitempty"`
	Goal    string `json:"goal,omitempty"`
	Comment string `json:"comment,omitempty"`
}

func signupHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !allow(ip) {
			writeJSON(w, http.StatusTooManyRequests, "слишком часто, попробуйте через минуту")
			return
		}

		var req signupRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, "некорректный JSON")
			return
		}

		req.Name = clean(req.Name, 200)
		req.Contact = clean(req.Contact, 200)
		req.Course = clean(req.Course, 200)
		req.Dates = clean(req.Dates, 1000)
		req.Level = clean(req.Level, 100)
		req.Goal = clean(req.Goal, 200)
		req.Comment = clean(req.Comment, 1000)

		if req.Name == "" || req.Contact == "" {
			writeJSON(w, http.StatusBadRequest, "заполните имя и контакт")
			return
		}

		var b strings.Builder
		add := func(label, value string) {
			if value != "" {
				fmt.Fprintf(&b, "%s: %s\r\n", label, value)
			}
		}
		add("Имя", req.Name)
		add("Контакт", req.Contact)
		add("Что интересует", req.Course)
		add("Даты и время", req.Dates)
		add("Уровень", req.Level)
		add("Цель", req.Goal)
		add("Комментарий", req.Comment)
		add("IP", ip)
		add("Время", time.Now().Format("2006-01-02 15:04:05"))

		if err := sendMail(cfg, "Новая заявка с сайта golang-mentor", b.String()); err != nil {
			log.Printf("ошибка отправки письма: %v", err)
			writeJSON(w, http.StatusInternalServerError, "не удалось отправить заявку, напишите на почту")
			return
		}
		log.Printf("заявка от %q (%s) отправлена", req.Name, req.Contact)
		writeJSON(w, http.StatusOK, "")
	}
}

// writeJSON пишет единообразный JSON-ответ: {"ok": true} либо {"ok": false, "error": "..."}.
func writeJSON(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	resp := map[string]any{"ok": errMsg == ""}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// clean обрезает пробелы и ограничивает длину строки.
func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// sendMail отправляет письмо через SMTP Яндекса (порт 465, implicit TLS).
func sendMail(cfg config, subject, body string) error {
	addr := net.JoinHostPort(cfg.SMTPHost, cfg.SMTPPort)

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.SMTPHost})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	auth := smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := c.Mail(cfg.SMTPUser); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(cfg.MailTo); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	msg := strings.Join([]string{
		"From: " + cfg.SMTPUser,
		"To: " + cfg.MailTo,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")

	if _, err := wc.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return c.Quit()
}
