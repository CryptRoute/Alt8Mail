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
	Env                   string
	Listen                string
	DatabaseURL           string
	SessionSecret         string
	CSRFSecret            string
	SecretKey             string
	PublicURL             string
	MailHostname          string
	WebmailURL            string
	WebPushKeyFile        string
	DNSResolver           string
	MailLogPath           string
	VmailRoot             string
	DKIMDir               string
	PostfixDir            string
	DovecotDir            string
	SSLDir                string
	TLSCertFile           string
	TLSKeyFile            string
	BackupDir             string
	CronFile              string
	CertbotDir            string
	CertbotWebroot        string
	NginxSitePath         string
	NginxEnabledPath      string
	NginxLogFormat        string
	NginxLogDir           string
	WebRoot               string
	WebNetRoot            string
	AppEnvFile            string
	WebUpstream           string
	AdminUpstream         string
	WebHealthURL          string
	AdminHealthURL        string
	SMTPAddr              string
	PolicyListen          string
	RedisAddr             string
	RedisPasswordFile     string
	OfficeAgentURL        string
	OfficeAgentToken      string
	OfficeImage           string
	OfficeSecretFile      string
	LicenseFile           string
	LicensePublicKey      string
	LicenseGroup          string
	LicenseWarningPercent int
	IngestInterval        time.Duration
	HealthInterval        time.Duration
	MetricsInterval       time.Duration
	RetentionDays         int
	TrustedProxy          bool
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
		MailHostname:      env("CONTROL_MAIL_HOSTNAME", "mail.a8m.test"),
		WebmailURL:        strings.TrimRight(env("CONTROL_WEBMAIL_URL", ""), "/"),
		WebPushKeyFile:    env("CONTROL_WEB_PUSH_KEY_FILE", "/etc/a8m/web_push_vapid.key"),
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
		NginxSitePath:     env("CONTROL_NGINX_SITE", "/etc/nginx/sites-available/a8m"),
		NginxEnabledPath:  env("CONTROL_NGINX_ENABLED", "/etc/nginx/sites-enabled/a8m"),
		NginxLogFormat:    env("CONTROL_NGINX_LOG_FORMAT", "/etc/nginx/conf.d/a8m-webnet-log.conf"),
		NginxLogDir:       env("CONTROL_NGINX_LOG_DIR", "/var/log/nginx"),
		WebRoot:           env("CONTROL_WEB_ROOT", "/var/www/a8m"),
		WebNetRoot:        env("CONTROL_WEB_NET_ROOT", "/var/www/a8m-webnet"),
		AppEnvFile:        env("CONTROL_APP_ENV", "/etc/a8m/app.env"),
		WebUpstream:       env("CONTROL_WEB_UPSTREAM", "127.0.0.1:8080"),
		AdminUpstream:     env("CONTROL_ADMIN_UPSTREAM", "127.0.0.1:8081"),
		WebHealthURL:      env("CONTROL_WEB_HEALTH_URL", "http://127.0.0.1:8080/api/health"),
		AdminHealthURL:    env("CONTROL_ADMIN_HEALTH_URL", "http://127.0.0.1:8081/api/health"),
		SMTPAddr:          env("CONTROL_SMTP_ADDR", "127.0.0.1:25"),
		PolicyListen:      env("CONTROL_POLICY_LISTEN", "127.0.0.1:10040"),
		RedisAddr:         env("CONTROL_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPasswordFile: env("CONTROL_REDIS_PASSWORD_FILE", "/opt/a8m/secrets/redis_password"),
		OfficeAgentURL:    strings.TrimRight(env("CONTROL_OFFICE_AGENT_URL", "http://127.0.0.1:8098"), "/"),
		OfficeAgentToken:  env("CONTROL_OFFICE_AGENT_TOKEN_FILE", "/etc/a8m/office-agent-token"),
		OfficeImage:       env("CONTROL_OFFICE_IMAGE", "onlyoffice/documentserver:9.4.0.1"),
		OfficeSecretFile:  env("CONTROL_OFFICE_JWT_SECRET_FILE", "/etc/a8m/office-jwt-secret"),
		// The Control Plane and webmail must point at the same license file so
		// an upload here is the file the webmail loads after its next restart.
		LicenseFile:           env("CONTROL_LICENSE_FILE", "/etc/a8m/license.token"),
		LicensePublicKey:      strings.TrimSpace(os.Getenv("CONTROL_LICENSE_PUBLIC_KEY")),
		LicenseGroup:          env("CONTROL_LICENSE_GROUP", "a8m"),
		LicenseWarningPercent: 80,
		IngestInterval:        envDuration("CONTROL_INGEST_INTERVAL", 8*time.Second),
		HealthInterval:        envDuration("CONTROL_HEALTH_INTERVAL", 15*time.Second),
		MetricsInterval:       envDuration("CONTROL_METRICS_INTERVAL", 5*time.Second),
		RetentionDays:         envInt("CONTROL_RETENTION_DAYS", 365),
		TrustedProxy:          envBool("CONTROL_TRUSTED_PROXY", false),
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
	// A missing license file or public key is a valid OSS deployment and must
	// never stop System Control from starting; only a malformed percentage is
	// a hard configuration error. An explicitly set value must parse: 0 and
	// non-numeric input are rejected instead of silently becoming 80.
	if raw := strings.TrimSpace(os.Getenv("CONTROL_LICENSE_WARNING_PERCENT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return cfg, fmt.Errorf("CONTROL_LICENSE_WARNING_PERCENT must be a number between 1 and 100")
		}
		cfg.LicenseWarningPercent = parsed
	}
	if cfg.LicenseWarningPercent < 1 || cfg.LicenseWarningPercent > 100 {
		return cfg, fmt.Errorf("CONTROL_LICENSE_WARNING_PERCENT must be between 1 and 100")
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
