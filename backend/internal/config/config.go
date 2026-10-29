package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env               string
	Listen            string
	PublicURL         string
	MailDomain        string
	MailHostname      string
	MaildirBase       string
	DKIMDir           string
	DKIMSelectorMap   string
	PublicDir         string
	UploadDir         string
	DatabaseURL       string
	DBMaxConns        int32
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	SessionSecret     string
	CSRFSecret        string
	CookieSecure      bool
	SessionTTL        time.Duration
	RememberTTL       time.Duration
	StaticCacheBust   string
	ReadHeaderTimeout time.Duration

	IMAPAddr              string
	IMAPUser              string
	IMAPPassword          string
	IMAPMasterUser        string
	IMAPMasterPassword    string
	IMAPTLSName           string
	SMTPAddr              string
	SMTPTLSName           string
	ManageSieveAddr       string
	ManageSieveTLSName    string
	MailCAFile            string
	IMAPSyncInterval      time.Duration
	IMAPPollInterval      time.Duration
	IMAPIdle              bool
	MailWorkerConcurrency int
	ClamAVAddr            string

	DemoMode      bool
	DemoEmail     string
	DemoPassword  string
	DemoUserEmail string

	WebAuthnRPID    string
	WebAuthnOrigins []string

	S3Endpoint            string
	S3Region              string
	S3Bucket              string
	S3AccessKey           string
	S3SecretKey           string
	S3UseSSL              bool
	S3PathStyle           bool
	GoogleClientID        string
	GoogleClientSecret    string
	DropboxClientID       string
	DropboxClientSecret   string
	MicrosoftClientID     string
	MicrosoftClientSecret string

	AdminListen       string
	AdminDir          string
	AdminPublicURL    string
	AdminSessionTTL   time.Duration
	AdminRateLimitMax int
	AdminRateWindow   time.Duration
}

func Load() (Config, error) {
	dkimDir := env("ALT8MAIL_DKIM_DIR", "/var/lib/rspamd/dkim")
	cfg := Config{
		Env:                   env("ALT8MAIL_ENV", "development"),
		Listen:                env("ALT8MAIL_LISTEN", "127.0.0.1:8080"),
		PublicURL:             env("ALT8MAIL_PUBLIC_URL", "http://localhost:8001"),
		MailDomain:            env("ALT8MAIL_MAIL_DOMAIN", "a8m.test"),
		MailHostname:          strings.TrimSuffix(strings.ToLower(env("ALT8MAIL_MAIL_HOSTNAME", "mail.a8m.test")), "."),
		MaildirBase:           env("ALT8MAIL_MAILDIR_BASE", "/var/vmail"),
		DKIMDir:               dkimDir,
		DKIMSelectorMap:       env("ALT8MAIL_DKIM_SELECTOR_MAP", filepath.Join(dkimDir, "selectors.map")),
		PublicDir:             env("ALT8MAIL_PUBLIC_DIR", "./public"),
		UploadDir:             env("ALT8MAIL_UPLOAD_DIR", "./data/uploads"),
		RedisAddr:             env("ALT8MAIL_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:         os.Getenv("ALT8MAIL_REDIS_PASSWORD"),
		SessionSecret:         os.Getenv("ALT8MAIL_SESSION_SECRET"),
		CSRFSecret:            os.Getenv("ALT8MAIL_CSRF_SECRET"),
		CookieSecure:          envBool("ALT8MAIL_COOKIE_SECURE", false),
		StaticCacheBust:       os.Getenv("ALT8MAIL_BUILD_ID"),
		ReadHeaderTimeout:     10 * time.Second,
		SessionTTL:            12 * time.Hour,
		RememberTTL:           30 * 24 * time.Hour,
		IMAPAddr:              env("ALT8MAIL_IMAP_ADDR", "127.0.0.1:993"),
		IMAPUser:              os.Getenv("ALT8MAIL_IMAP_USER"),
		IMAPMasterUser:        os.Getenv("ALT8MAIL_IMAP_MASTER_USER"),
		IMAPTLSName:           env("ALT8MAIL_IMAP_TLS_NAME", "mail.a8m.test"),
		SMTPAddr:              env("ALT8MAIL_SMTP_ADDR", "127.0.0.1:587"),
		SMTPTLSName:           env("ALT8MAIL_SMTP_TLS_NAME", "mail.a8m.test"),
		ManageSieveAddr:       env("ALT8MAIL_MANAGESIEVE_ADDR", "127.0.0.1:4190"),
		MailCAFile:            os.Getenv("ALT8MAIL_MAIL_CA_FILE"),
		DemoMode:              envBool("ALT8MAIL_DEMO_MODE", false),
		DemoEmail:             strings.ToLower(env("ALT8MAIL_DEMO_EMAIL", "")),
		DemoPassword:          os.Getenv("ALT8MAIL_DEMO_PASSWORD"),
		DemoUserEmail:         strings.ToLower(env("ALT8MAIL_DEMO_USER_EMAIL", "")),
		IMAPSyncInterval:      5 * time.Minute,
		IMAPPollInterval:      15 * time.Second,
		IMAPIdle:              envBool("ALT8MAIL_IMAP_IDLE", true),
		MailWorkerConcurrency: 8,
		ClamAVAddr:            os.Getenv("ALT8MAIL_CLAMAV_ADDR"),
		S3Endpoint:            env("ALT8MAIL_S3_ENDPOINT", ""),
		S3Region:              env("ALT8MAIL_S3_REGION", "us-east-1"),
		S3Bucket:              env("ALT8MAIL_S3_BUCKET", ""),
		S3AccessKey:           os.Getenv("ALT8MAIL_S3_ACCESS_KEY"),
		S3SecretKey:           os.Getenv("ALT8MAIL_S3_SECRET_KEY"),
		S3UseSSL:              envBool("ALT8MAIL_S3_USE_SSL", true),
		S3PathStyle:           envBool("ALT8MAIL_S3_PATH_STYLE", true),
		GoogleClientID:        os.Getenv("ALT8MAIL_GOOGLE_CLIENT_ID"),
		GoogleClientSecret:    os.Getenv("ALT8MAIL_GOOGLE_CLIENT_SECRET"),
		DropboxClientID:       os.Getenv("ALT8MAIL_DROPBOX_CLIENT_ID"),
		DropboxClientSecret:   os.Getenv("ALT8MAIL_DROPBOX_CLIENT_SECRET"),
		MicrosoftClientID:     os.Getenv("ALT8MAIL_MICROSOFT_CLIENT_ID"),
		MicrosoftClientSecret: os.Getenv("ALT8MAIL_MICROSOFT_CLIENT_SECRET"),
		AdminListen:           env("ALT8MAIL_ADMIN_LISTEN", "127.0.0.1:8081"),
		AdminDir:              env("ALT8MAIL_ADMIN_DIR", "./admin"),
		AdminPublicURL:        strings.TrimRight(env("ALT8MAIL_ADMIN_URL", ""), "/"),
		AdminSessionTTL:       8 * time.Hour,
		AdminRateLimitMax:     10,
		AdminRateWindow:       10 * time.Minute,
	}
	if hours, err := strconv.Atoi(env("ALT8MAIL_ADMIN_SESSION_HOURS", "8")); err == nil && hours >= 1 && hours <= 72 {
		cfg.AdminSessionTTL = time.Duration(hours) * time.Hour
	}
	if limit, err := strconv.Atoi(env("ALT8MAIL_ADMIN_RATE_LIMIT", "10")); err == nil && limit >= 3 && limit <= 100 {
		cfg.AdminRateLimitMax = limit
	}
	if minutes, err := strconv.Atoi(env("ALT8MAIL_ADMIN_RATE_WINDOW_MINUTES", "10")); err == nil && minutes >= 1 && minutes <= 120 {
		cfg.AdminRateWindow = time.Duration(minutes) * time.Minute
	}
	publicURL, _ := url.Parse(cfg.PublicURL)
	defaultRPID := publicURL.Hostname()
	if defaultRPID == "" {
		defaultRPID = "localhost"
	}
	cfg.ManageSieveTLSName = env("ALT8MAIL_MANAGESIEVE_TLS_NAME", cfg.IMAPTLSName)
	cfg.WebAuthnRPID = env("ALT8MAIL_WEBAUTHN_RPID", defaultRPID)
	origins := strings.TrimSpace(os.Getenv("ALT8MAIL_WEBAUTHN_ORIGINS"))
	if origins == "" {
		cfg.WebAuthnOrigins = []string{strings.TrimRight(cfg.PublicURL, "/")}
	} else {
		for _, origin := range strings.Split(origins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				cfg.WebAuthnOrigins = append(cfg.WebAuthnOrigins, strings.TrimRight(origin, "/"))
			}
		}
	}
	if seconds, err := strconv.Atoi(env("ALT8MAIL_IMAP_SYNC_SECONDS", "300")); err == nil && seconds >= 30 {
		cfg.IMAPSyncInterval = time.Duration(seconds) * time.Second
	}
	if seconds, err := strconv.Atoi(env("ALT8MAIL_IMAP_POLL_SECONDS", "15")); err == nil && seconds >= 5 {
		cfg.IMAPPollInterval = time.Duration(seconds) * time.Second
	}
	if workers, err := strconv.Atoi(env("ALT8MAIL_MAIL_WORKER_CONCURRENCY", "8")); err == nil && workers >= 1 && workers <= 64 {
		cfg.MailWorkerConcurrency = workers
	}
	if file := strings.TrimSpace(os.Getenv("ALT8MAIL_IMAP_PASSWORD_FILE")); file != "" {
		password, err := os.ReadFile(file)
		if err != nil {
			return Config{}, fmt.Errorf("read ALT8MAIL_IMAP_PASSWORD_FILE: %w", err)
		}
		cfg.IMAPPassword = strings.TrimSpace(string(password))
	} else {
		cfg.IMAPPassword = os.Getenv("ALT8MAIL_IMAP_PASSWORD")
	}
	if file := strings.TrimSpace(os.Getenv("ALT8MAIL_IMAP_MASTER_PASSWORD_FILE")); file != "" {
		password, err := os.ReadFile(file)
		if err != nil {
			return Config{}, fmt.Errorf("read ALT8MAIL_IMAP_MASTER_PASSWORD_FILE: %w", err)
		}
		cfg.IMAPMasterPassword = strings.TrimSpace(string(password))
	} else {
		cfg.IMAPMasterPassword = os.Getenv("ALT8MAIL_IMAP_MASTER_PASSWORD")
	}

	dbHost := env("ALT8MAIL_DB_HOST", "127.0.0.1")
	dbPort := env("ALT8MAIL_DB_PORT", "5432")
	dbName := env("ALT8MAIL_DB_NAME", "a8m")
	dbUser := env("ALT8MAIL_DB_USER", "a8m_app")
	dbPassword := os.Getenv("ALT8MAIL_DB_PASSWORD")
	sslMode := env("ALT8MAIL_DB_SSLMODE", "disable")
	cfg.DatabaseURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		dbUser, urlEscape(dbPassword), dbHost, dbPort, dbName, sslMode)

	maxConns, err := strconv.Atoi(env("ALT8MAIL_DB_MAX_CONNS", "20"))
	if err != nil || maxConns < 1 {
		return Config{}, fmt.Errorf("ALT8MAIL_DB_MAX_CONNS must be a positive integer")
	}
	cfg.DBMaxConns = int32(maxConns)
	cfg.RedisDB, _ = strconv.Atoi(env("ALT8MAIL_REDIS_DB", "0"))

	if cfg.SessionSecret == "" || cfg.CSRFSecret == "" {
		return Config{}, fmt.Errorf("ALT8MAIL_SESSION_SECRET and ALT8MAIL_CSRF_SECRET are required")
	}
	if cfg.Env == "production" && !cfg.CookieSecure {
		return Config{}, fmt.Errorf("ALT8MAIL_COOKIE_SECURE must be true in production")
	}
	if cfg.Env == "production" && cfg.DemoMode {
		return Config{}, fmt.Errorf("staging demo login cannot be enabled in production")
	}
	if cfg.DemoMode && (cfg.DemoEmail == "" || cfg.DemoPassword == "" || cfg.DemoUserEmail == "") {
		return Config{}, fmt.Errorf("ALT8MAIL_DEMO_EMAIL, ALT8MAIL_DEMO_PASSWORD and ALT8MAIL_DEMO_USER_EMAIL are required when demo mode is on")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// urlEscape escapes a password for use in a URL DSN. pgx accepts URL DSNs; the
// password is the only user-controlled component that commonly contains '@',
// ':' or '/'. Keep the escaping local so deployments cannot leak it in logs.
func urlEscape(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25", " ", "%20", "@", "%40", ":", "%3A", "/", "%2F",
		"?", "%3F", "#", "%23", "&", "%26", "+", "%2B", "=", "%3D",
	)
	return replacer.Replace(value)
}
