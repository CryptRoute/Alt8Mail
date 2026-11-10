package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap"
)

// mailImportOAuthToken is the credential bundle accepted in the import row's
// password field for Gmail API and Microsoft Graph jobs. A raw access token is
// also accepted for short migrations; refresh tokens are required for jobs
// that outlive the provider's access-token lifetime.
type mailImportOAuthToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

func parseMailImportOAuthToken(plain string) mailImportOAuthToken {
	plain = strings.TrimSpace(plain)
	if strings.HasPrefix(plain, "{") {
		var token mailImportOAuthToken
		if json.Unmarshal([]byte(plain), &token) == nil {
			return token
		}
	}
	return mailImportOAuthToken{AccessToken: plain}
}

func (s *Server) importHTTP() *http.Client {
	if s.importHTTPClient != nil {
		return s.importHTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Server) gmailBase() string {
	if value := strings.TrimRight(strings.TrimSpace(s.gmailBaseURL), "/"); value != "" {
		return value
	}
	return "https://gmail.googleapis.com"
}

func (s *Server) graphBase() string {
	if value := strings.TrimRight(strings.TrimSpace(s.graphBaseURL), "/"); value != "" {
		return value
	}
	return "https://graph.microsoft.com/v1.0"
}

func (s *Server) importOAuthAccessToken(ctx context.Context, protocol, plain string) (string, error) {
	credentials := parseMailImportOAuthToken(plain)
	if strings.TrimSpace(credentials.AccessToken) != "" {
		return strings.TrimSpace(credentials.AccessToken), nil
	}
	if credentials.RefreshToken == "" || credentials.ClientID == "" || credentials.ClientSecret == "" {
		return "", fmt.Errorf("the OAuth token is missing an access token or a refresh-token bundle")
	}
	endpoint := ""
	form := url.Values{
		"client_id":     {credentials.ClientID},
		"client_secret": {credentials.ClientSecret},
		"refresh_token": {credentials.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	if protocol == "gmail" {
		endpoint = strings.TrimSpace(s.gmailTokenURL)
		if endpoint == "" {
			endpoint = "https://oauth2.googleapis.com/token"
		}
	} else {
		endpoint = strings.TrimSpace(s.graphTokenURL)
		if endpoint == "" {
			endpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
		}
		form.Set("scope", "https://graph.microsoft.com/.default offline_access")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.importHTTP().Do(request)
	if err != nil {
		return "", fmt.Errorf("could not refresh the %s token", protocol)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("%s rejected the refresh token", protocol)
	}
	var refreshed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &refreshed); err != nil || refreshed.AccessToken == "" {
		return "", fmt.Errorf("%s returned an unusable access token", protocol)
	}
	return refreshed.AccessToken, nil
}

func (s *Server) importOAuthJSON(ctx context.Context, protocol, endpoint, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-Migration/1.0")
	response, err := s.importHTTP().Do(request)
	if err != nil {
		return fmt.Errorf("could not reach %s", protocol)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", protocol, response.StatusCode)
	}
	return json.Unmarshal(raw, target)
}

func (s *Server) importOAuthBytes(ctx context.Context, protocol, endpoint, token string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "message/rfc822, application/octet-stream")
	request.Header.Set("User-Agent", "Alt8Mail-Migration/1.0")
	response, err := s.importHTTP().Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s", protocol)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", protocol, response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errMailImportMessageTooLarge
	}
	return raw, nil
}

func (s *Server) importOAuthProfile(ctx context.Context, protocol, token string) (string, error) {
	if protocol == "gmail" {
		var profile struct {
			EmailAddress string `json:"emailAddress"`
		}
		if err := s.importOAuthJSON(ctx, protocol, s.gmailBase()+"/gmail/v1/users/me/profile", token, &profile); err != nil {
			return "", err
		}
		return strings.ToLower(strings.TrimSpace(profile.EmailAddress)), nil
	}
	var profile struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := s.importOAuthJSON(ctx, protocol, s.graphBase()+"/me?$select=mail,userPrincipalName", token, &profile); err != nil {
		return "", err
	}
	account := strings.TrimSpace(profile.Mail)
	if account == "" {
		account = strings.TrimSpace(profile.UserPrincipalName)
	}
	return strings.ToLower(account), nil
}

func importCursorValue(cursorJSON, key string) int64 {
	inner := map[string]any{}
	_ = json.Unmarshal([]byte(cursorJSON), &inner)
	if value, ok := inner[key].(float64); ok {
		return int64(value)
	}
	return 0
}

// ensureImportTarget selects a destination folder, creating it when the
// account does not have it yet.
func ensureImportTarget(target interface {
	Select(string, bool) (*imap.MailboxStatus, error)
	Create(string) error
}, folder string) error {
	if _, err := target.Select(folder, false); err == nil {
		return nil
	}
	if err := target.Create(folder); err != nil {
		return err
	}
	_, err := target.Select(folder, false)
	return err
}

func (s *Server) importGmailMailbox(ctx context.Context, job mailImportJobRecord, sourceEmail, plain, targetEmail, cursorJSON, itemID string) (int64, int64, error) {
	token, err := s.importOAuthAccessToken(ctx, "gmail", plain)
	if err != nil {
		s.mailImportEvent(ctx, job.ID, itemID, "error", "source", "Gmail token is unavailable", err.Error())
		return 0, 0, err
	}
	account, err := s.importOAuthProfile(ctx, "gmail", token)
	if err != nil {
		return 0, 0, err
	}
	if account != "" && !strings.EqualFold(account, sourceEmail) {
		return 0, 0, fmt.Errorf("the Gmail token belongs to %s, not %s", account, sourceEmail)
	}
	target, err := s.dialLocalTarget(targetEmail)
	if err != nil {
		s.mailImportEvent(ctx, job.ID, itemID, "error", "target", "Could not open the destination mailbox", err.Error())
		return 0, 0, err
	}
	defer target.Logout()
	cursor := map[string]int64{"gmailAfter": importCursorValue(cursorJSON, "gmailAfter")}
	maxBytes := mailImportMaxBytes(job.Options)
	existing := map[string]map[string]bool{}
	existingFor := func(folder string) map[string]bool {
		if existing[folder] == nil {
			existing[folder] = map[string]bool{}
			if ids, err := targetFolderMessageIDs(target, folder); err == nil {
				existing[folder] = ids
			}
		}
		return existing[folder]
	}
	var imported, total, quarantined int64
	pageToken := ""
	for pages := 0; pages < 100; pages++ {
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return imported, total, errMailImportCancelled
		}
		query := url.Values{"maxResults": {"100"}}
		if cursor["gmailAfter"] > 0 {
			query.Set("q", fmt.Sprintf("after:%d", cursor["gmailAfter"]/1000))
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var page struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := s.importOAuthJSON(ctx, "gmail", s.gmailBase()+"/gmail/v1/users/me/messages?"+query.Encode(), token, &page); err != nil {
			return imported, total, err
		}
		for _, listed := range page.Messages {
			if s.mailImportStatus(ctx, job.ID) == "cancelled" {
				return imported, total, errMailImportCancelled
			}
			total++
			var message struct {
				ID           string   `json:"id"`
				LabelIDs     []string `json:"labelIds"`
				InternalDate string   `json:"internalDate"`
				Raw          string   `json:"raw"`
			}
			if err := s.importOAuthJSON(ctx, "gmail", s.gmailBase()+"/gmail/v1/users/me/messages/"+url.PathEscape(listed.ID)+"?format=raw", token, &message); err != nil {
				s.mailImportEvent(ctx, job.ID, itemID, "warn", "gmail", "Could not fetch a Gmail message", err.Error())
				continue
			}
			if stamp, err := parseInt64String(message.InternalDate); err == nil && stamp > cursor["gmailAfter"] {
				cursor["gmailAfter"] = stamp
			}
			raw, decodeErr := decodeGmailRaw(message.Raw)
			if decodeErr != nil || len(raw) == 0 || int64(len(raw)) > maxBytes {
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			folder, flags := gmailImportFolder(message.LabelIDs)
			received := time.Now()
			if stamp, err := parseInt64String(message.InternalDate); err == nil && stamp > 0 {
				received = time.Unix(stamp/1000, 0)
			}
			messageID := strings.ToLower(strings.TrimSpace(rawMessageID(raw)))
			if messageID != "" && existingFor(folder)[messageID] {
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			quarantine, err := s.scanImportedMessage(ctx, target, raw, flags, received, sourceEmail, messageID)
			if err != nil {
				return imported, total, err
			}
			if quarantine {
				quarantined++
				if messageID != "" {
					existingFor(folder)[messageID] = true
				}
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			if err := ensureImportTarget(target, folder); err != nil {
				s.mailImportEvent(ctx, job.ID, itemID, "warn", "target", "Could not open destination folder "+folder, err.Error())
				continue
			}
			if err := target.Append(folder, flags, received, bytes.NewReader(raw)); err != nil {
				s.mailImportEvent(ctx, job.ID, itemID, "warn", "gmail", "Could not append a Gmail message to "+folder, err.Error())
				continue
			}
			imported++
			if messageID != "" {
				existingFor(folder)[messageID] = true
			}
			s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}
	if quarantined > 0 {
		s.mailImportEvent(ctx, job.ID, itemID, "warn", "antivirus",
			fmt.Sprintf("Quarantined %d infected message(s)", quarantined), "Destination: "+importQuarantineMailbox)
	}
	return imported, total, nil
}

func parseInt64String(value string) (int64, error) {
	var parsed int64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed)
	return parsed, err
}

func decodeGmailRaw(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("empty Gmail message")
	}
	if raw, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.URLEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

func gmailImportFolder(labels []string) (string, []string) {
	folder := "INBOX"
	for _, label := range labels {
		switch strings.ToUpper(strings.TrimSpace(label)) {
		case "SENT":
			folder = "Sent"
		case "DRAFT":
			folder = "Drafts"
		case "TRASH":
			folder = "Trash"
		case "SPAM":
			folder = "Junk"
		}
	}
	flags := []string{}
	unread := false
	for _, label := range labels {
		switch strings.ToUpper(strings.TrimSpace(label)) {
		case "UNREAD":
			unread = true
		case "STARRED":
			flags = append(flags, `\Flagged`)
		}
	}
	if !unread {
		flags = append(flags, `\Seen`)
	}
	return folder, flags
}

func (s *Server) importGraphMailbox(ctx context.Context, job mailImportJobRecord, sourceEmail, plain, targetEmail, cursorJSON, itemID string) (int64, int64, error) {
	token, err := s.importOAuthAccessToken(ctx, "graph", plain)
	if err != nil {
		s.mailImportEvent(ctx, job.ID, itemID, "error", "source", "Microsoft Graph token is unavailable", err.Error())
		return 0, 0, err
	}
	account, err := s.importOAuthProfile(ctx, "graph", token)
	if err != nil {
		return 0, 0, err
	}
	if account != "" && !strings.EqualFold(account, sourceEmail) {
		return 0, 0, fmt.Errorf("the Microsoft token belongs to %s, not %s", account, sourceEmail)
	}
	target, err := s.dialLocalTarget(targetEmail)
	if err != nil {
		s.mailImportEvent(ctx, job.ID, itemID, "error", "target", "Could not open the destination mailbox", err.Error())
		return 0, 0, err
	}
	defer target.Logout()
	cursor := map[string]int64{"graphSince": importCursorValue(cursorJSON, "graphSince")}
	maxBytes := mailImportMaxBytes(job.Options)
	folderNames := map[string]string{}
	var folderPage struct {
		Value []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"displayName"`
			WellKnownName string `json:"wellKnownName"`
		} `json:"value"`
	}
	if err := s.importOAuthJSON(ctx, "graph", s.graphBase()+"/me/mailFolders?$top=100&$select=id,displayName,wellKnownName", token, &folderPage); err == nil {
		for _, folder := range folderPage.Value {
			folderNames[folder.ID] = graphFolderName(folder.WellKnownName, folder.DisplayName)
		}
	}
	existing := map[string]map[string]bool{}
	existingFor := func(folder string) map[string]bool {
		if existing[folder] == nil {
			existing[folder] = map[string]bool{}
			if ids, err := targetFolderMessageIDs(target, folder); err == nil {
				existing[folder] = ids
			}
		}
		return existing[folder]
	}
	var imported, total, quarantined int64
	next := s.graphBase() + "/me/messages?$top=50&$select=id,internetMessageId,receivedDateTime,isRead,flag,parentFolderId"
	if since := cursor["graphSince"]; since > 0 {
		next += "&$filter=" + url.QueryEscape("receivedDateTime gt "+time.Unix(since, 0).UTC().Format(time.RFC3339))
	}
	for pages := 0; pages < 100 && next != ""; pages++ {
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return imported, total, errMailImportCancelled
		}
		var page struct {
			Value []struct {
				ID                string `json:"id"`
				InternetMessageID string `json:"internetMessageId"`
				ReceivedDateTime  string `json:"receivedDateTime"`
				IsRead            bool   `json:"isRead"`
				Flag              struct {
					FlagStatus string `json:"flagStatus"`
				} `json:"flag"`
				ParentFolderID string `json:"parentFolderId"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := s.importOAuthJSON(ctx, "graph", next, token, &page); err != nil {
			return imported, total, err
		}
		for _, listed := range page.Value {
			if s.mailImportStatus(ctx, job.ID) == "cancelled" {
				return imported, total, errMailImportCancelled
			}
			total++
			received, _ := time.Parse(time.RFC3339, listed.ReceivedDateTime)
			if received.Unix() > cursor["graphSince"] {
				cursor["graphSince"] = received.Unix()
			}
			raw, err := s.importOAuthBytes(ctx, "graph", s.graphBase()+"/me/messages/"+url.PathEscape(listed.ID)+"/$value", token, maxBytes)
			if err == errMailImportMessageTooLarge {
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			if err != nil || len(raw) == 0 {
				if err != nil {
					s.mailImportEvent(ctx, job.ID, itemID, "warn", "graph", "Could not fetch a Microsoft message", err.Error())
				}
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			folder := graphFolderName("", "")
			if name, ok := folderNames[listed.ParentFolderID]; ok {
				folder = name
			}
			messageID := strings.ToLower(strings.TrimSpace(listed.InternetMessageID))
			if messageID == "" {
				messageID = strings.ToLower(strings.TrimSpace(rawMessageID(raw)))
			}
			if messageID != "" && existingFor(folder)[messageID] {
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			flags := []string{}
			if listed.IsRead {
				flags = append(flags, `\Seen`)
			}
			if strings.EqualFold(listed.Flag.FlagStatus, "flagged") {
				flags = append(flags, `\Flagged`)
			}
			if received.IsZero() {
				received = time.Now()
			}
			quarantine, err := s.scanImportedMessage(ctx, target, raw, flags, received, sourceEmail, messageID)
			if err != nil {
				return imported, total, err
			}
			if quarantine {
				quarantined++
				if messageID != "" {
					existingFor(folder)[messageID] = true
				}
				s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
				continue
			}
			if err := ensureImportTarget(target, folder); err != nil {
				s.mailImportEvent(ctx, job.ID, itemID, "warn", "target", "Could not open destination folder "+folder, err.Error())
				continue
			}
			if err := target.Append(folder, flags, received, bytes.NewReader(raw)); err != nil {
				s.mailImportEvent(ctx, job.ID, itemID, "warn", "graph", "Could not append a Microsoft message to "+folder, err.Error())
				continue
			}
			imported++
			if messageID != "" {
				existingFor(folder)[messageID] = true
			}
			s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
		}
		next = page.NextLink
		if next != "" && !s.graphNextLinkAllowed(next) {
			return imported, total, fmt.Errorf("Microsoft Graph returned an unexpected next link")
		}
	}
	if quarantined > 0 {
		s.mailImportEvent(ctx, job.ID, itemID, "warn", "antivirus",
			fmt.Sprintf("Quarantined %d infected message(s)", quarantined), "Destination: "+importQuarantineMailbox)
	}
	return imported, total, nil
}

func graphFolderName(wellKnown, displayName string) string {
	switch strings.ToLower(strings.TrimSpace(wellKnown)) {
	case "inbox":
		return "INBOX"
	case "sentitems":
		return "Sent"
	case "drafts":
		return "Drafts"
	case "deleteditems":
		return "Trash"
	case "junkemail":
		return "Junk"
	case "archive":
		return "Archive"
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		return "INBOX"
	}
	return truncate(name, 80)
}

func (s *Server) graphNextLinkAllowed(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	if s.graphBaseURL != "" {
		base, err := url.Parse(s.graphBase())
		return err == nil && strings.EqualFold(parsed.Host, base.Host)
	}
	return strings.EqualFold(parsed.Hostname(), "graph.microsoft.com")
}
