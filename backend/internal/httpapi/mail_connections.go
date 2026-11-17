package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

const (
	externalIMAPTimeout = 30 * time.Second
	externalSMTPTimeout = 45 * time.Second
)

type publicMailDialer struct {
	host    string
	port    int
	timeout time.Duration
}

func (d publicMailDialer) Dial(_, _ string) (net.Conn, error) {
	return dialPublicMail(context.Background(), d.host, d.port, d.timeout)
}

func validMailHostname(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return true
	}
	if strings.ContainsAny(host, " /\\\t\r\n@:") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

var blockedMailPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("64:ff9b::/96"),
}

func publicMailAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return false
	}
	if addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return false
	}
	for _, prefix := range blockedMailPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return addr.IsGlobalUnicast()
}

func resolvePublicMailIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	if !validMailHostname(host) {
		return nil, fmt.Errorf("enter a valid mail server hostname")
	}
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		addr = addr.Unmap()
		if !publicMailAddress(addr) {
			return nil, fmt.Errorf("mail server addresses on private or local networks are not allowed")
		}
		return []netip.Addr{addr}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("could not resolve the mail server hostname: %w", err)
	}
	public := make([]netip.Addr, 0, len(addresses))
	for _, addr := range addresses {
		addr = addr.Unmap()
		if publicMailAddress(addr) {
			public = append(public, addr)
		}
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("mail server addresses on private or local networks are not allowed")
	}
	return public, nil
}

func validateMailPort(port int) error {
	if port == 25 {
		return fmt.Errorf("port 25 is not allowed for connected mail accounts")
	}
	switch port {
	case 143, 465, 587, 993:
		return nil
	}
	if port >= 1024 && port <= 65535 {
		return nil
	}
	return fmt.Errorf("choose a standard mail port (143, 465, 587 or 993) or a port above 1024")
}

func validateMailTLSMode(mode string, implicit bool) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		if implicit {
			return "tls", nil
		}
		return "starttls", nil
	}
	if mode != "tls" && mode != "starttls" {
		return "", fmt.Errorf("TLS is required for connected mail accounts")
	}
	return mode, nil
}

func validateMailEndpoint(ctx context.Context, host string, port int) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	if err := validateMailPort(port); err != nil {
		return "", err
	}
	if _, err := resolvePublicMailIPs(ctx, host); err != nil {
		return "", err
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		host = addr.String()
	}
	return host, nil
}

func dialPublicMail(ctx context.Context, host string, port int, timeout time.Duration) (net.Conn, error) {
	addresses, err := resolvePublicMailIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	var lastErr error
	for _, addr := range addresses {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no routable mail server address")
	}
	return nil, lastErr
}

func externalMailTLSConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

type externalMailAuth struct {
	Username    string
	Password    string
	AccessToken string
}

func (auth externalMailAuth) oauth() bool {
	return strings.TrimSpace(auth.AccessToken) != ""
}

// xoauth2IMAPClient implements the SASL XOAUTH2 mechanism used by Google and
// Microsoft IMAP. The initial response is the documented user/auth bearer
// payload; an error challenge is answered once with an empty response so the
// server can return its final NO.
type xoauth2IMAPClient struct {
	username   string
	token      string
	challenged bool
	challenge  []byte
}

func (client *xoauth2IMAPClient) Start() (string, []byte, error) {
	payload := "user=" + client.username + "\x01auth=Bearer " + client.token + "\x01\x01"
	return "XOAUTH2", []byte(payload), nil
}

func (client *xoauth2IMAPClient) Next(challenge []byte) ([]byte, error) {
	if !client.challenged {
		client.challenged = true
		// Gmail puts the required scope in this JSON challenge. Keep it so a
		// failed login can explain whether the token or the mailbox is at fault.
		client.challenge = append([]byte(nil), challenge...)
		return []byte{}, nil
	}
	return nil, fmt.Errorf("the mail server rejected the OAuth token")
}

func xoauth2ChallengeDetail(challenge []byte) string {
	var payload struct {
		Status string `json:"status"`
		Scope  string `json:"scope"`
	}
	if json.Unmarshal(challenge, &payload) != nil || strings.TrimSpace(payload.Scope) == "" {
		return ""
	}
	return fmt.Sprintf(" (the server requested scope %s)", strings.TrimSpace(payload.Scope))
}

// transientExternalMailError separates a provider hiccup from a credential
// problem. Gmail uses "Temporary system problem" for rate limits and backend
// incidents; treating that as a reconnect requirement strands the account
// because the sync worker only retries accounts that are not in reauth.
func transientExternalMailError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"temporary system problem", "temporary failure", "temporarily unavailable",
		"try again later", "timeout", "i/o timeout", "connection reset", "connection refused",
		"connection closed", "unexpected eof", "use of closed network connection",
		"broken pipe", "connection aborted", "no such host", "network is unreachable", "server busy",
		"too many", "rate limit", "service unavailable", "bad gateway", "gateway timeout",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func externalMailAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"invalid credentials", "authentication failed", "invalid_grant",
		"authorization expired", "needs to be reconnected", "unauthorized",
		"invalid token",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// "login failed" is Gmail's outer wording for both expired tokens and
	// transient IMAP incidents. The transient check runs first in the caller.
	return strings.Contains(message, "login failed") || strings.Contains(message, "authenticate")
}

func externalSyncFailureStatus(err error) string {
	if !transientExternalMailError(err) && externalMailAuthFailure(err) {
		return "reauth"
	}
	return "error"
}

// xoauth2SMTPAuth implements the net/smtp Auth interface for XOAUTH2.
type xoauth2SMTPAuth struct {
	username   string
	token      string
	challenged bool
}

func (auth *xoauth2SMTPAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil || !server.TLS {
		return "", nil, fmt.Errorf("XOAUTH2 requires a TLS connection")
	}
	payload := "user=" + auth.username + "\x01auth=Bearer " + auth.token + "\x01\x01"
	return "XOAUTH2", []byte(payload), nil
}

func (auth *xoauth2SMTPAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	if !auth.challenged {
		auth.challenged = true
		return []byte{}, nil
	}
	return nil, fmt.Errorf("the mail server rejected the OAuth token")
}

func (s *Server) accountMailAuth(ctx context.Context, account mailAccountRecord) (externalMailAuth, error) {
	username := strings.TrimSpace(account.Username)
	if username == "" {
		username = strings.TrimSpace(account.Email)
	}
	if account.AuthType == "oauth2" {
		token, err := s.mailOAuthAccessToken(ctx, account)
		if err != nil {
			return externalMailAuth{}, err
		}
		return externalMailAuth{Username: username, AccessToken: token}, nil
	}
	password, err := s.accountPassword(ctx, account)
	if err != nil {
		return externalMailAuth{}, err
	}
	return externalMailAuth{Username: username, Password: password}, nil
}

func (s *Server) openExternalIMAP(ctx context.Context, account mailAccountRecord) (*imapclient.Client, error) {
	auth, err := s.accountMailAuth(ctx, account)
	if err != nil {
		return nil, err
	}
	return openExternalIMAPWithAuth(ctx, account.IMAPHost, account.IMAPPort, account.IMAPTLS, auth)
}

func openExternalIMAPWithPassword(ctx context.Context, host string, port int, tlsMode, username, password string) (*imapclient.Client, error) {
	return openExternalIMAPWithAuth(ctx, host, port, tlsMode, externalMailAuth{Username: username, Password: password})
}

func openExternalIMAPWithAuth(ctx context.Context, host string, port int, tlsMode string, auth externalMailAuth) (*imapclient.Client, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := openExternalIMAPWithAuthOnce(ctx, host, port, tlsMode, auth)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		// Gmail occasionally drops the connection during the XOAUTH2 exchange
		// (or immediately after a long idle period). One fresh connection is
		// enough to tell a provider hiccup from a real credential problem.
		if !transientExternalMailError(err) || ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

func openExternalIMAPWithAuthOnce(ctx context.Context, host string, port int, tlsMode string, auth externalMailAuth) (*imapclient.Client, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if auth.Username == "" {
		return nil, fmt.Errorf("mail account has no username")
	}
	if err := validateMailPort(port); err != nil {
		return nil, err
	}
	if _, err := resolvePublicMailIPs(ctx, host); err != nil {
		return nil, err
	}
	tlsConfig := externalMailTLSConfig(host)
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := publicMailDialer{host: host, port: port, timeout: 15 * time.Second}
	var conn *imapclient.Client
	var err error
	if strings.EqualFold(tlsMode, "starttls") {
		conn, err = imapclient.DialWithDialer(dialer, address)
		if err == nil {
			if startErr := conn.StartTLS(tlsConfig); startErr != nil {
				_ = conn.Logout()
				conn = nil
				err = startErr
			}
		}
	} else {
		conn, err = imapclient.DialWithDialerTLS(dialer, address, tlsConfig)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to the IMAP server: %w", err)
	}
	conn.Timeout = externalIMAPTimeout
	if auth.oauth() {
		oauthClient := &xoauth2IMAPClient{username: auth.Username, token: auth.AccessToken}
		err = conn.Authenticate(oauthClient)
		if err != nil {
			_ = conn.Logout()
			detail := ""
			if !transientExternalMailError(err) {
				detail = xoauth2ChallengeDetail(oauthClient.challenge)
			}
			return nil, fmt.Errorf("IMAP login failed: %w%s", err, detail)
		}
	} else {
		err = conn.Login(auth.Username, auth.Password)
	}
	if err != nil {
		_ = conn.Logout()
		return nil, fmt.Errorf("IMAP login failed: %w", err)
	}
	return conn, nil
}

func (s *Server) openExternalSMTP(ctx context.Context, account mailAccountRecord) (*smtp.Client, error) {
	auth, err := s.accountMailAuth(ctx, account)
	if err != nil {
		return nil, err
	}
	return openExternalSMTPWithAuth(ctx, account.SMTPHost, account.SMTPPort, account.SMTPTLS, account.Username, auth)
}

func openExternalSMTPWithPassword(ctx context.Context, host string, port int, tlsMode, username, password string) (*smtp.Client, error) {
	return openExternalSMTPWithAuth(ctx, host, port, tlsMode, username, externalMailAuth{Username: username, Password: password})
}

func openExternalSMTPWithAuth(ctx context.Context, host string, port int, tlsMode, username string, auth externalMailAuth) (*smtp.Client, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if auth.Username == "" {
		auth.Username = strings.TrimSpace(username)
	}
	if auth.Username == "" {
		return nil, fmt.Errorf("mail account has no username")
	}
	if err := validateMailPort(port); err != nil {
		return nil, err
	}
	if _, err := resolvePublicMailIPs(ctx, host); err != nil {
		return nil, err
	}
	tlsConfig := externalMailTLSConfig(host)
	conn, err := dialPublicMail(ctx, host, port, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("could not connect to the SMTP server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(externalSMTPTimeout))
	if strings.EqualFold(tlsMode, "tls") {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("SMTP TLS handshake failed: %w", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SMTP greeting failed: %w", err)
	}
	if strings.EqualFold(tlsMode, "starttls") {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, fmt.Errorf("the SMTP server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP STARTTLS failed: %w", err)
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		_ = client.Close()
		return nil, fmt.Errorf("the SMTP server does not offer authentication")
	}
	if auth.oauth() {
		if ok, _ := client.Extension("XOAUTH2"); !ok {
			_ = client.Close()
			return nil, fmt.Errorf("the SMTP server does not offer XOAUTH2 authentication")
		}
		err = client.Auth(&xoauth2SMTPAuth{username: auth.Username, token: auth.AccessToken})
	} else {
		err = client.Auth(smtp.PlainAuth("", auth.Username, auth.Password, host))
	}
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("SMTP authentication failed: %w", err)
	}
	return client, nil
}

func (s *Server) accountPassword(ctx context.Context, account mailAccountRecord) (string, error) {
	sealed, err := s.mailAccountSealedSecret(ctx, account.UserID, account.ID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(sealed) == "" {
		return "", fmt.Errorf("this account needs to be reconnected")
	}
	password, err := s.openMailAccountSecret(sealed)
	if err != nil {
		return "", fmt.Errorf("could not unlock the mail account credential")
	}
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("this account needs to be reconnected")
	}
	return password, nil
}

func (s *Server) testExternalMailConnection(ctx context.Context, account mailAccountRecord) error {
	auth, err := s.accountMailAuth(ctx, account)
	if err != nil {
		return err
	}
	return s.testExternalMailConnectionWithAuth(ctx, account, auth)
}

func (s *Server) testExternalMailConnectionWithPassword(ctx context.Context, account mailAccountRecord, password string) error {
	return s.testExternalMailConnectionWithAuth(ctx, account, externalMailAuth{
		Username: firstNonEmpty(account.Username, account.Email), Password: password,
	})
}

func (s *Server) testExternalMailConnectionWithAuth(ctx context.Context, account mailAccountRecord, auth externalMailAuth) error {
	conn, err := openExternalIMAPWithAuth(ctx, account.IMAPHost, account.IMAPPort, account.IMAPTLS, auth)
	if err != nil {
		return err
	}
	if _, listErr := listIMAPMailboxes(conn); listErr != nil {
		_ = conn.Logout()
		return fmt.Errorf("IMAP folder listing failed: %w", listErr)
	}
	_ = conn.Logout()
	client, err := openExternalSMTPWithAuth(ctx, account.SMTPHost, account.SMTPPort, account.SMTPTLS, account.Username, auth)
	if err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
