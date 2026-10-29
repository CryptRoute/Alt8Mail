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
	dkimDir := env("MERIDIAN_DKIM_DIR", "/var/lib/rspamd/dkim")
	cfg := Config{
		Env:                   env("MERIDIAN_ENV", "development"),
		Listen:                env("MERIDIAN_LISTEN", "127.0.0.1:8080"),
		PublicURL:             env("MERIDIAN_PUBLIC_URL", "http://localhost:8001"),
		MailDomain:            env("MERIDIAN_MAIL_DOMAIN", "meridian.test"),
		MailHostname:          strings.TrimSuffix(strings.ToLower(env("MERIDIAN_MAIL_HOSTNAME", "mail.meridian.test")), "."),
		MaildirBase:           env("MERIDIAN_MAILDIR_BASE", "/var/vmail"),
		DKIMDir:               dkimDir,
		DKIMSelectorMap:       env("MERIDIAN_DKIM_SELECTOR_MAP", filepath.Join(dkimDir, "selectors.map")),
		PublicDir:             env("MERIDIAN_PUBLIC_DIR", "./public"),
		UploadDir:             env("MERIDIAN_UPLOAD_DIR", "./data/uploads"),
		RedisAddr:             env("MERIDIAN_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:         os.Getenv("MERIDIAN_REDIS_PASSWORD"),
		SessionSecret:         os.Getenv("MERIDIAN_SESSION_SECRET"),
		CSRFSecret:            os.Getenv("MERIDIAN_CSRF_SECRET"),
		CookieSecure:          envBool("MERIDIAN_COOKIE_SECURE", false),
		StaticCacheBust:       os.Getenv("MERIDIAN_BUILD_ID"),
		ReadHeaderTimeout:     10 * time.Second,
		SessionTTL:            12 * time.Hour,
		RememberTTL:           30 * 24 * time.Hour,
		IMAPAddr:              env("MERIDIAN_IMAP_ADDR", "127.0.0.1:993"),
		IMAPUser:              os.Getenv("MERIDIAN_IMAP_USER"),
		IMAPMasterUser:        os.Getenv("MERIDIAN_IMAP_MASTER_USER"),
		IMAPTLSName:           env("MERIDIAN_IMAP_TLS_NAME", "mail.meridian.test"),
		SMTPAddr:              env("MERIDIAN_SMTP_ADDR", "127.0.0.1:587"),
		SMTPTLSName:           env("MERIDIAN_SMTP_TLS_NAME", "mail.meridian.test"),
		ManageSieveAddr:       env("MERIDIAN_MANAGESIEVE_ADDR", "127.0.0.1:4190"),
		MailCAFile:            os.Getenv("MERIDIAN_MAIL_CA_FILE"),
		DemoMode:              envBool("MERIDIAN_DEMO_MODE", false),
		DemoEmail:             strings.ToLower(env("MERIDIAN_DEMO_EMAIL", "")),
		DemoPassword:          os.Getenv("MERIDIAN_DEMO_PASSWORD"),
		DemoUserEmail:         strings.ToLower(env("MERIDIAN_DEMO_USER_EMAIL", "")),
		IMAPSyncInterval:      5 * time.Minute,
		IMAPPollInterval:      15 * time.Second,
		IMAPIdle:              envBool("MERIDIAN_IMAP_IDLE", true),
		MailWorkerConcurrency: 8,
		ClamAVAddr:            os.Getenv("MERIDIAN_CLAMAV_ADDR"),
		S3Endpoint:            env("MERIDIAN_S3_ENDPOINT", ""),
		S3Region:              env("MERIDIAN_S3_REGION", "us-east-1"),
		S3Bucket:              env("MERIDIAN_S3_BUCKET", ""),
		S3AccessKey:           os.Getenv("MERIDIAN_S3_ACCESS_KEY"),
		S3SecretKey:           os.Getenv("MERIDIAN_S3_SECRET_KEY"),
		S3UseSSL:              envBool("MERIDIAN_S3_USE_SSL", true),
		S3PathStyle:           envBool("MERIDIAN_S3_PATH_STYLE", true),
		GoogleClientID:        os.Getenv("MERIDIAN_GOOGLE_CLIENT_ID"),
		GoogleClientSecret:    os.Getenv("MERIDIAN_GOOGLE_CLIENT_SECRET"),
		DropboxClientID:       os.Getenv("MERIDIAN_DROPBOX_CLIENT_ID"),
		DropboxClientSecret:   os.Getenv("MERIDIAN_DROPBOX_CLIENT_SECRET"),
		MicrosoftClientID:     os.Getenv("MERIDIAN_MICROSOFT_CLIENT_ID"),
		MicrosoftClientSecret: os.Getenv("MERIDIAN_MICROSOFT_CLIENT_SECRET"),
		AdminListen:           env("MERIDIAN_ADMIN_LISTEN", "127.0.0.1:8081"),
		AdminDir:              env("MERIDIAN_ADMIN_DIR", "./admin"),
		AdminPublicURL:        strings.TrimRight(env("MERIDIAN_ADMIN_URL", ""), "/"),
		AdminSessionTTL:       8 * time.Hour,
		AdminRateLimitMax:     10,
		AdminRateWindow:       10 * time.Minute,
	}
	if hours, err := strconv.Atoi(env("MERIDIAN_ADMIN_SESSION_HOURS", "8")); err == nil && hours >= 1 && hours <= 72 {
		cfg.AdminSessionTTL = time.Duration(hours) * time.Hour
	}
	if limit, err := strconv.Atoi(env("MERIDIAN_ADMIN_RATE_LIMIT", "10")); err == nil && limit >= 3 && limit <= 100 {
		cfg.AdminRateLimitMax = limit
	}
	if minutes, err := strconv.Atoi(env("MERIDIAN_ADMIN_RATE_WINDOW_MINUTES", "10")); err == nil && minutes >= 1 && minutes <= 120 {
		cfg.AdminRateWindow = time.Duration(minutes) * time.Minute
	}
	publicURL, _ := url.Parse(cfg.PublicURL)
	defaultRPID := publicURL.Hostname()
	if defaultRPID == "" {
		defaultRPID = "localhost"
	}
	cfg.ManageSieveTLSName = env("MERIDIAN_MANAGESIEVE_TLS_NAME", cfg.IMAPTLSName)
	cfg.WebAuthnRPID = env("MERIDIAN_WEBAUTHN_RPID", defaultRPID)
	origins := strings.TrimSpace(os.Getenv("MERIDIAN_WEBAUTHN_ORIGINS"))
	if origins == "" {
		cfg.WebAuthnOrigins = []string{strings.TrimRight(cfg.PublicURL, "/")}
	} else {
		for _, origin := range strings.Split(origins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				cfg.WebAuthnOrigins = append(cfg.WebAuthnOrigins, strings.TrimRight(origin, "/"))
			}
		}
	}
	if seconds, err := strconv.Atoi(env("MERIDIAN_IMAP_SYNC_SECONDS", "300")); err == nil && seconds >= 30 {
		cfg.IMAPSyncInterval = time.Duration(seconds) * time.Second
	}
	if seconds, err := strconv.Atoi(env("MERIDIAN_IMAP_POLL_SECONDS", "15")); err == nil && seconds >= 5 {
		cfg.IMAPPollInterval = time.Duration(seconds) * time.Second
	}
	if workers, err := strconv.Atoi(env("MERIDIAN_MAIL_WORKER_CONCURRENCY", "8")); err == nil && workers >= 1 && workers <= 64 {
		cfg.MailWorkerConcurrency = workers
	}
	if file := strings.TrimSpace(os.Getenv("MERIDIAN_IMAP_PASSWORD_FILE")); file != "" {
		password, err := os.ReadFile(file)
		if err != nil {
			return Config{}, fmt.Errorf("read MERIDIAN_IMAP_PASSWORD_FILE: %w", err)
		}
		cfg.IMAPPassword = strings.TrimSpace(string(password))
	} else {
		cfg.IMAPPassword = os.Getenv("MERIDIAN_IMAP_PASSWORD")
	}
	if file := strings.TrimSpace(os.Getenv("MERIDIAN_IMAP_MASTER_PASSWORD_FILE")); file != "" {
		password, err := os.ReadFile(file)
		if err != nil {
			return Config{}, fmt.Errorf("read MERIDIAN_IMAP_MASTER_PASSWORD_FILE: %w", err)
		}
		cfg.IMAPMasterPassword = strings.TrimSpace(string(password))
	} else {
		cfg.IMAPMasterPassword = os.Getenv("MERIDIAN_IMAP_MASTER_PASSWORD")
	}

	dbHost := env("MERIDIAN_DB_HOST", "127.0.0.1")
	dbPort := env("MERIDIAN_DB_PORT", "5432")
	dbName := env("MERIDIAN_DB_NAME", "meridian")
	dbUser := env("MERIDIAN_DB_USER", "meridian_app")
	dbPassword := os.Getenv("MERIDIAN_DB_PASSWORD")
	sslMode := env("MERIDIAN_DB_SSLMODE", "disable")
	cfg.DatabaseURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		dbUser, urlEscape(dbPassword), dbHost, dbPort, dbName, sslMode)

	maxConns, err := strconv.Atoi(env("MERIDIAN_DB_MAX_CONNS", "20"))
	if err != nil || maxConns < 1 {
		return Config{}, fmt.Errorf("MERIDIAN_DB_MAX_CONNS must be a positive integer")
	}
	cfg.DBMaxConns = int32(maxConns)
	cfg.RedisDB, _ = strconv.Atoi(env("MERIDIAN_REDIS_DB", "0"))

	if cfg.SessionSecret == "" || cfg.CSRFSecret == "" {
		return Config{}, fmt.Errorf("MERIDIAN_SESSION_SECRET and MERIDIAN_CSRF_SECRET are required")
	}
	if cfg.Env == "production" && !cfg.CookieSecure {
		return Config{}, fmt.Errorf("MERIDIAN_COOKIE_SECURE must be true in production")
	}
	if cfg.Env == "production" && cfg.DemoMode {
		return Config{}, fmt.Errorf("staging demo login cannot be enabled in production")
	}
	if cfg.DemoMode && (cfg.DemoEmail == "" || cfg.DemoPassword == "" || cfg.DemoUserEmail == "") {
		return Config{}, fmt.Errorf("MERIDIAN_DEMO_EMAIL, MERIDIAN_DEMO_PASSWORD and MERIDIAN_DEMO_USER_EMAIL are required when demo mode is on")
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
