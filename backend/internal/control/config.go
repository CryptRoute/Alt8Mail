package control

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is intentionally environment-driven: the control plane is a separate
// service and must never share the webmail or customer-admin secret material.
type Config struct {
	Env               string
	Listen            string
	DatabaseURL       string
	SessionSecret     string
	CSRFSecret        string
	SecretKey         string
	PublicURL         string
	MailHostname      string
	WebmailURL        string
	DNSResolver       string
	MailLogPath       string
	VmailRoot         string
	DKIMDir           string
	PostfixDir        string
	DovecotDir        string
	SSLDir            string
	TLSCertFile       string
	TLSKeyFile        string
	BackupDir         string
	CronFile          string
	CertbotDir        string
	CertbotWebroot    string
	SMTPAddr          string
	PolicyListen      string
	RedisPasswordFile string
	IngestInterval    time.Duration
	HealthInterval    time.Duration
	MetricsInterval   time.Duration
	RetentionDays     int
	TrustedProxy      bool
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Env:               env("CONTROL_ENV", "staging"),
		Listen:            env("CONTROL_LISTEN", "0.0.0.0:8808"),
		DatabaseURL:       os.Getenv("CONTROL_DATABASE_URL"),
		SessionSecret:     os.Getenv("CONTROL_SESSION_SECRET"),
		CSRFSecret:        os.Getenv("CONTROL_CSRF_SECRET"),
		SecretKey:         os.Getenv("CONTROL_SECRET_KEY"),
		PublicURL:         strings.TrimRight(env("CONTROL_PUBLIC_URL", "https://130.94.46.22:8808"), "/"),
		MailHostname:      env("CONTROL_MAIL_HOSTNAME", "mail.meridian.test"),
		WebmailURL:        strings.TrimRight(env("CONTROL_WEBMAIL_URL", ""), "/"),
		DNSResolver:       env("CONTROL_DNS_RESOLVER", "1.1.1.1:53"),
		MailLogPath:       env("CONTROL_MAIL_LOG", "/var/log/mail.log"),
		VmailRoot:         env("CONTROL_VMAIL_ROOT", "/var/vmail"),
		DKIMDir:           env("CONTROL_DKIM_DIR", "/var/lib/rspamd/dkim"),
		PostfixDir:        env("CONTROL_POSTFIX_DIR", "/etc/postfix"),
		DovecotDir:        env("CONTROL_DOVECOT_DIR", "/etc/dovecot"),
		SSLDir:            env("CONTROL_SSL_DIR", "/etc/ssl/alt8mail"),
		TLSCertFile:       env("CONTROL_TLS_CERT", "/etc/ssl/alt8mail/control.crt"),
		TLSKeyFile:        env("CONTROL_TLS_KEY", "/etc/ssl/alt8mail/control.key"),
		BackupDir:         env("CONTROL_BACKUP_DIR", "/var/backups/alt8mail"),
		CronFile:          env("CONTROL_CRON_FILE", "/etc/cron.d/alt8mail"),
		CertbotDir:        env("CONTROL_CERTBOT_DIR", "/etc/letsencrypt/live"),
		CertbotWebroot:    env("CONTROL_CERTBOT_WEBROOT", "/var/www/certbot"),
		SMTPAddr:          env("CONTROL_SMTP_ADDR", "127.0.0.1:25"),
		PolicyListen:      env("CONTROL_POLICY_LISTEN", "127.0.0.1:10040"),
		RedisPasswordFile: env("CONTROL_REDIS_PASSWORD_FILE", "/opt/meridian/secrets/redis_password"),
		IngestInterval:    envDuration("CONTROL_INGEST_INTERVAL", 8*time.Second),
		HealthInterval:    envDuration("CONTROL_HEALTH_INTERVAL", 15*time.Second),
		MetricsInterval:   envDuration("CONTROL_METRICS_INTERVAL", 5*time.Second),
		RetentionDays:     envInt("CONTROL_RETENTION_DAYS", 365),
		TrustedProxy:      envBool("CONTROL_TRUSTED_PROXY", false),
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("CONTROL_DATABASE_URL is required")
	}
	if len(cfg.SessionSecret) < 32 || len(cfg.CSRFSecret) < 32 || len(cfg.SecretKey) < 32 {
		return cfg, fmt.Errorf("CONTROL_SESSION_SECRET, CONTROL_CSRF_SECRET and CONTROL_SECRET_KEY must each be at least 32 characters")
	}
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 3650 {
		return cfg, fmt.Errorf("CONTROL_RETENTION_DAYS must be between 1 and 3650")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value == 0 {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
