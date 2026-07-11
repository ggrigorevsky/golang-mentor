// Сервер сайта golang-mentor: раздаёт статику и принимает заявки из формы,
// отправляя их письмом через SMTP Яндекса.
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

func signupHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !allow(ip) {
			http.Error(w, "слишком часто, попробуйте через минуту", http.StatusTooManyRequests)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "некорректная форма", http.StatusBadRequest)
			return
		}
		name := clean(r.FormValue("name"), 200)
		contact := clean(r.FormValue("contact"), 200)
		course := clean(r.FormValue("course"), 200)
		dates := clean(r.FormValue("dates"), 1000)
		if name == "" || contact == "" {
			http.Error(w, "заполните имя и контакт", http.StatusBadRequest)
			return
		}

		body := fmt.Sprintf(
			"Имя: %s\r\nКонтакт: %s\r\nЧто интересует: %s\r\nДаты и время: %s\r\nIP: %s\r\nВремя: %s\r\n",
			name, contact, course, dates, ip, time.Now().Format("2006-01-02 15:04:05"),
		)
		if err := sendMail(cfg, "Новая заявка с сайта golang-mentor", body); err != nil {
			log.Printf("ошибка отправки письма: %v", err)
			http.Error(w, "не удалось отправить заявку, напишите на почту", http.StatusInternalServerError)
			return
		}
		log.Printf("заявка от %q (%s) отправлена", name, contact)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}
}

// clean обрезает строку и убирает переводы строк из однострочных полей.
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
