package httpapi

import (
	"fmt"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

// dialLocalTarget connects to the deployment's local IMAP server as the
// mailbox owner. It is used by private modules that need to read or append to
// a mailbox on the user's behalf.
func (s *Server) dialLocalTarget(targetEmail string) (*imapclient.Client, error) {
	loginUser, loginPassword, err := s.mailLoginForUser(targetEmail)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return nil, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to the Alt8Mail mail server: %w", err)
	}
	conn.Timeout = 90 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		conn.Logout()
		return nil, fmt.Errorf("Alt8Mail mailbox login: %w", err)
	}
	ensureIMAPFolders(conn)
	return conn, nil
}
