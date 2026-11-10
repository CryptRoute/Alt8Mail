package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// scimUserBodyLimit caps every SCIM Users request body. The limit is
	// deliberately small: SCIM payloads are identity records, not content.
	scimUserBodyLimit    int64 = 1024 * 1024
	scimUserDefaultCount       = 100
	scimUserMaxCount           = 500
	// scimUserProvisionQuota matches the platform default quota assigned by
	// the other provisioning paths.
	scimUserProvisionQuota int64 = 16 * 1024 * 1024 * 1024
)

// scimUserColumns is the projection shared by every Users query. deleted_at is
// selected so a soft-deleted account is never exposed through SCIM.
const scimUserColumns = `id::text, email::text, name, given_name, family_name,
	scim_external_id, role, status, created_at, updated_at, deleted_at`

// scimUserRow is an internal row. It is never marshalled directly; the SCIM
// resource shape is built by scimUserResource.
type scimUserRow struct {
	ID         string
	Email      string
	Name       string
	GivenName  string
	FamilyName string
	ExternalID *string
	Role       string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// scimHandlerError carries a SCIM error envelope. It lets pure parsing helpers
// report status/scimType without writing a response themselves.
type scimHandlerError struct {
	status   int
	scimType string
	detail   string
}

func (e *scimHandlerError) Error() string { return e.detail }

func newSCIMHandlerError(status int, scimType, detail string) *scimHandlerError {
	return &scimHandlerError{status: status, scimType: scimType, detail: detail}
}

func writeSCIMHandlerError(w http.ResponseWriter, err error) {
	var handlerErr *scimHandlerError
	if errors.As(err, &handlerErr) {
		writeSCIMError(w, handlerErr.status, handlerErr.scimType, handlerErr.detail)
		return
	}
	writeSCIMError(w, http.StatusInternalServerError, "", "Could not complete the SCIM request")
}

// scimUserPayload is the subset of the RFC 7643 User resource this server
// understands. Unknown attributes (notably password, activation, groups and
// enterprise extensions) are intentionally ignored: SCIM never syncs
// credentials and never manages roles.
type scimUserPayload struct {
	Schemas     []string               `json:"schemas"`
	ID          string                 `json:"id"`
	ExternalID  scimNullableString     `json:"externalId"`
	UserName    *string                `json:"userName"`
	Name        *scimUserNamePayload   `json:"name"`
	DisplayName *string                `json:"displayName"`
	Emails      []scimUserEmailPayload `json:"emails"`
	Active      *bool                  `json:"active"`
	Roles       []map[string]any       `json:"roles"`
	Meta        json.RawMessage        `json:"meta"`
}

// scimNullableString distinguishes an omitted attribute (Set false) from an
// explicit JSON null (Set with a nil Value), which PUT needs in order to clear
// externalId.
type scimNullableString struct {
	Set   bool
	Value *string
}

func (value *scimNullableString) UnmarshalJSON(data []byte) error {
	value.Set = true
	if string(bytes.TrimSpace(data)) == "null" {
		value.Value = nil
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	value.Value = &text
	return nil
}

type scimUserNamePayload struct {
	Formatted  *string `json:"formatted"`
	GivenName  *string `json:"givenName"`
	FamilyName *string `json:"familyName"`
}

type scimUserEmailPayload struct {
	Value   string `json:"value"`
	Type    string `json:"type"`
	Primary *bool  `json:"primary"`
}

type scimPatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type scimPatchRequest struct {
	Schemas    []string             `json:"schemas"`
	Operations []scimPatchOperation `json:"Operations"`
}

// scimUserChanges is the validated, not-yet-persisted mutation produced by PUT
// and PATCH. Nil pointers mean "leave unchanged"; ExternalIDSet distinguishes
// "clear" (true with nil ExternalID) from "unchanged" (false).
type scimUserChanges struct {
	Name          *string
	GivenName     *string
	FamilyName    *string
	ExternalIDSet bool
	ExternalID    *string
	ActiveSet     bool
	Active        bool
}

type scimUserTransition struct {
	MetadataChanged bool
	Deactivated     bool
	Reactivated     bool
}

// metadataChanged reports whether name or externalId values actually differ
// from the stored row. Active-only no-ops must not be audited as updates.
func (changes scimUserChanges) metadataChanged(existing scimUserRow) bool {
	if changes.Name != nil && *changes.Name != existing.Name {
		return true
	}
	if changes.GivenName != nil && *changes.GivenName != existing.GivenName {
		return true
	}
	if changes.FamilyName != nil && *changes.FamilyName != existing.FamilyName {
		return true
	}
	if changes.ExternalIDSet && !sameSCIMExternalID(changes.ExternalID, existing.ExternalID) {
		return true
	}
	return false
}

// scimUserAuditEvents lists the fixed actions for one successful mutation.
// Metadata changes are audited in addition to lifecycle transitions.
func scimUserAuditEvents(transition scimUserTransition) []string {
	actions := []string{}
	if transition.MetadataChanged {
		actions = append(actions, "scim.user_updated")
	}
	if transition.Deactivated {
		actions = append(actions, "scim.user_deactivated")
	}
	if transition.Reactivated {
		actions = append(actions, "scim.user_reactivated")
	}
	return actions
}

// decodeSCIMRequest accepts the two SCIM JSON media types, caps the body at
// 1 MiB and rejects unknown-free JSON. Unknown *fields* are allowed because IdP
// clients routinely send read-only attributes (schemas, meta, groups).
func decodeSCIMRequest(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" && mediaType != "application/scim+json" {
		return errors.New("Content-Type must be application/scim+json or application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, scimUserBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.New("Request body must be valid JSON under 1 MiB")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("Request body is required")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("Request body must be valid JSON")
	}
	return nil
}

// parseSCIMFilter accepts exactly one "<attribute> eq \"<value>\"" expression.
// Attributes are matched case-insensitively against the allowlist, and the
// canonical allowlist spelling is returned so callers can switch on it.
func parseSCIMFilter(raw string, allowed map[string]bool) (field, value string, err error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", "", errors.New("empty filter")
	}
	index := 0
	for index < len(text) && text[index] != ' ' && text[index] != '\t' {
		index++
	}
	attribute := text[:index]
	remainder := strings.TrimSpace(text[index:])
	if attribute == "" || remainder == "" {
		return "", "", errors.New("filter must be an attribute, operator and value")
	}
	canonical := ""
	for key := range allowed {
		if strings.EqualFold(key, attribute) {
			canonical = key
			break
		}
	}
	if canonical == "" {
		return "", "", errors.New("unsupported filter attribute")
	}
	index = 0
	for index < len(remainder) && remainder[index] != ' ' && remainder[index] != '\t' {
		index++
	}
	if !strings.EqualFold(remainder[:index], "eq") {
		return "", "", errors.New("only the eq filter operator is supported")
	}
	literal := strings.TrimSpace(remainder[index:])
	if len(literal) < 2 || literal[0] != '"' {
		return "", "", errors.New("filter value must be a quoted string")
	}
	decoded, err := strconv.Unquote(literal)
	if err != nil {
		return "", "", errors.New("filter value must be a quoted string")
	}
	return canonical, decoded, nil
}

// parseSCIMPagination applies SCIM defaults and caps count at 500. Zero is a
// valid count (an empty page); negative values are rejected.
func parseSCIMPagination(r *http.Request) (startIndex, count int, err error) {
	query := r.URL.Query()
	startIndex = 1
	if raw := strings.TrimSpace(query.Get("startIndex")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 {
			return 0, 0, errors.New("startIndex must be a positive integer")
		}
		startIndex = parsed
	}
	count = scimUserDefaultCount
	if raw := strings.TrimSpace(query.Get("count")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			return 0, 0, errors.New("count must be a non-negative integer")
		}
		count = parsed
	}
	if count > scimUserMaxCount {
		count = scimUserMaxCount
	}
	return startIndex, count, nil
}

// scimUserVersion is a weak ETag derived from the row's last modification
// time. It changes on every write because scimApplyUserChanges always touches
// updated_at.
func scimUserVersion(row scimUserRow) string {
	return `W/"` + strconv.FormatInt(row.UpdatedAt.UTC().UnixMicro(), 36) + `"`
}

// scimUserResource renders the RFC 7643 core User shape. externalId is omitted
// when SQL NULL, matching the optional attribute semantics.
func scimUserResource(row scimUserRow, base string) map[string]any {
	roles := []map[string]any{}
	if validUserRole(row.Role) {
		roles = append(roles, map[string]any{"value": row.Role})
	}
	resource := map[string]any{
		"schemas":  []string{scimUserSchema},
		"id":       row.ID,
		"userName": row.Email,
		"name": map[string]any{
			"formatted":  row.Name,
			"givenName":  row.GivenName,
			"familyName": row.FamilyName,
		},
		"displayName": row.Name,
		"emails": []map[string]any{{
			"value": row.Email, "type": "work", "primary": true,
		}},
		"active": row.Status == "active",
		"roles":  roles,
		"meta": map[string]any{
			"resourceType": "User",
			"created":      row.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": row.UpdatedAt.UTC().Format(time.RFC3339),
			"location":     base + "/Users/" + row.ID,
			"version":      scimUserVersion(row),
		},
	}
	if row.ExternalID != nil {
		resource["externalId"] = *row.ExternalID
	}
	return resource
}

func scanSCIMUser(row pgx.Row) (scimUserRow, error) {
	var user scimUserRow
	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.GivenName, &user.FamilyName,
		&user.ExternalID, &user.Role, &user.Status, &user.CreatedAt, &user.UpdatedAt, &user.DeletedAt)
	return user, err
}

// scimHasControlCharacters rejects C0 controls and DEL. These are never valid
// identity attributes and PostgreSQL rejects NUL outright, so they must fail
// as a SCIM validation error before a transaction starts.
func scimHasControlCharacters(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func validateSCIMText(field, value string) *scimHandlerError {
	if scimHasControlCharacters(value) {
		return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", field+" must not contain control characters")
	}
	return nil
}

func validateSCIMExternalIDText(raw *string) *scimHandlerError {
	if raw == nil {
		return nil
	}
	return validateSCIMText("externalId", *raw)
}

func validateSCIMNameText(formatted, given, family string) *scimHandlerError {
	fields := []struct {
		name  string
		value string
	}{
		{name: "name.formatted", value: formatted},
		{name: "name.givenName", value: given},
		{name: "name.familyName", value: family},
	}
	for _, field := range fields {
		if err := validateSCIMText(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

// scimUserName normalizes a SCIM userName. It must be an email-shaped value;
// the returned email is lowercased.
func scimUserName(raw *string) (email, localPart string, err error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "", "", errors.New("userName is required")
	}
	email = strings.ToLower(strings.TrimSpace(*raw))
	if strings.ContainsAny(email, " \t\r\n") || scimHasControlCharacters(email) {
		return "", "", errors.New("userName must be a valid email address")
	}
	localPart, domain := splitEmail(email)
	if localPart == "" || domain == "" {
		return "", "", errors.New("userName must be a valid email address")
	}
	return email, localPart, nil
}

// normalizeSCIMExternalID trims the value and maps the empty string to SQL
// NULL, per the phase ruling on externalId idempotency.
func normalizeSCIMExternalID(raw scimNullableString) *string {
	if !raw.Set || raw.Value == nil {
		return nil
	}
	return normalizeSCIMExternalIDValue(raw.Value)
}

func normalizeSCIMExternalIDValue(raw *string) *string {
	if raw == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// scimDisplayName derives users.name: name.formatted wins, then
// givenName+familyName, then the userName local part.
func scimDisplayName(name *scimUserNamePayload, fallback string) (formatted, given, family string) {
	if name != nil {
		if name.Formatted != nil {
			formatted = strings.TrimSpace(*name.Formatted)
		}
		if name.GivenName != nil {
			given = strings.TrimSpace(*name.GivenName)
		}
		if name.FamilyName != nil {
			family = strings.TrimSpace(*name.FamilyName)
		}
	}
	if formatted == "" {
		formatted = strings.TrimSpace(strings.Join([]string{given, family}, " "))
	}
	if formatted == "" {
		formatted = fallback
	}
	return formatted, given, family
}

// scimCreateDisposition classifies an existing tenant user during create.
type scimCreateDisposition int

const (
	scimCreateProceed scimCreateDisposition = iota
	scimCreateExisting
	scimCreateConflict
)

func sameSCIMExternalID(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// classifySCIMCreate implements replay idempotency: only the same
// externalId+userName pair (including both having no externalId) is a replay.
// A soft-deleted row never replays.
func classifySCIMCreate(existing *scimUserRow, externalID *string) scimCreateDisposition {
	if existing == nil {
		return scimCreateProceed
	}
	if existing.DeletedAt == nil && sameSCIMExternalID(existing.ExternalID, externalID) {
		return scimCreateExisting
	}
	return scimCreateConflict
}

// validateSCIMEmails enforces the immutable-email rule: every provided address
// must be the user's userName.
func validateSCIMEmails(emails []scimUserEmailPayload, userName string) *scimHandlerError {
	for _, email := range emails {
		if !strings.EqualFold(strings.TrimSpace(email.Value), userName) {
			return newSCIMHandlerError(http.StatusConflict, "mutability", "emails cannot be changed through SCIM; they must match userName")
		}
	}
	return nil
}

// scimReplaceChanges validates a PUT payload and produces the desired mutation.
func scimReplaceChanges(payload scimUserPayload, existing scimUserRow) (scimUserChanges, *scimHandlerError) {
	var changes scimUserChanges
	if payload.UserName != nil {
		email, _, err := scimUserName(payload.UserName)
		if err != nil {
			return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
		}
		if !strings.EqualFold(email, existing.Email) {
			return changes, newSCIMHandlerError(http.StatusConflict, "mutability", "userName cannot be changed through SCIM")
		}
	}
	if len(payload.Emails) > 0 {
		if err := validateSCIMEmails(payload.Emails, existing.Email); err != nil {
			return changes, err
		}
	}
	if payload.Name != nil {
		localPart, _ := splitEmail(existing.Email)
		formatted, given, family := scimDisplayName(payload.Name, localPart)
		if err := validateSCIMNameText(formatted, given, family); err != nil {
			return changes, err
		}
		changes.Name = &formatted
		changes.GivenName = &given
		changes.FamilyName = &family
	}
	if payload.ExternalID.Set {
		if err := validateSCIMExternalIDText(payload.ExternalID.Value); err != nil {
			return changes, err
		}
		changes.ExternalIDSet = true
		changes.ExternalID = normalizeSCIMExternalID(payload.ExternalID)
	}
	if payload.Active != nil {
		changes.ActiveSet = true
		changes.Active = *payload.Active
	}
	return changes, nil
}

func decodeSCIMString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("value must be a string")
	}
	return strings.TrimSpace(value), nil
}

// decodeSCIMNullableString returns null=true for a JSON null. The empty string
// is normalized to SQL NULL by callers.
func decodeSCIMNullableString(raw json.RawMessage) (value string, null bool, err error) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return "", true, nil
	}
	value, err = decodeSCIMString(raw)
	return value, false, err
}

func decodeSCIMBool(raw json.RawMessage) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("value must be a boolean")
	}
	return value, nil
}

// scimPatchChanges validates a PATCH payload and produces the desired mutation.
// It is pure so the add/replace/remove matrix is unit-testable without a DB.
func scimPatchChanges(payload scimPatchRequest, existing scimUserRow) (scimUserChanges, *scimHandlerError) {
	var changes scimUserChanges
	if len(payload.Operations) == 0 {
		return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH requires at least one operation")
	}
	localPart, _ := splitEmail(existing.Email)
	for _, operation := range payload.Operations {
		op := strings.ToLower(strings.TrimSpace(operation.Op))
		if op != "add" && op != "replace" && op != "remove" {
			return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH op must be add, replace or remove")
		}
		path := strings.TrimSpace(operation.Path)
		if path == "" {
			if op != "replace" {
				return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH without a path supports replace only")
			}
			if err := applySCIMPatchObject(&changes, operation.Value, existing, localPart); err != nil {
				return changes, err
			}
			continue
		}
		if err := applySCIMPatchPath(&changes, op, path, operation.Value, existing, localPart); err != nil {
			return changes, err
		}
	}
	return changes, nil
}

func applySCIMPatchObject(changes *scimUserChanges, raw json.RawMessage, existing scimUserRow, localPart string) *scimHandlerError {
	if len(bytes.TrimSpace(raw)) == 0 {
		return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH replace requires a value")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH replace value must be an object")
	}
	for key, value := range object {
		switch strings.ToLower(key) {
		case "username":
			email, err := decodeSCIMString(value)
			if err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			normalized, _, normalizeErr := scimUserName(&email)
			if normalizeErr != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", normalizeErr.Error())
			}
			if !strings.EqualFold(normalized, existing.Email) {
				return newSCIMHandlerError(http.StatusConflict, "mutability", "userName cannot be changed through SCIM")
			}
		case "emails":
			var emails []scimUserEmailPayload
			if err := json.Unmarshal(value, &emails); err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "emails must be an array of email objects")
			}
			if err := validateSCIMEmails(emails, existing.Email); err != nil {
				return err
			}
		case "name":
			var name scimUserNamePayload
			if err := json.Unmarshal(value, &name); err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "name must be an object")
			}
			formatted, given, family := scimDisplayName(&name, localPart)
			if err := validateSCIMNameText(formatted, given, family); err != nil {
				return err
			}
			changes.Name = &formatted
			changes.GivenName = &given
			changes.FamilyName = &family
		case "externalid":
			text, null, err := decodeSCIMNullableString(value)
			if err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			changes.ExternalIDSet = true
			if null {
				changes.ExternalID = nil
			} else if err := validateSCIMExternalIDText(&text); err != nil {
				return err
			} else if text == "" {
				changes.ExternalID = nil
			} else {
				changes.ExternalID = &text
			}
		case "active":
			value, err := decodeSCIMBool(value)
			if err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			changes.ActiveSet = true
			changes.Active = value
		case "schemas", "id", "meta":
			// Read-only resource envelope attributes are ignored when echoed.
		default:
			return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
		}
	}
	return nil
}

func applySCIMPatchPath(changes *scimUserChanges, op, path string, raw json.RawMessage, existing scimUserRow, localPart string) *scimHandlerError {
	segments := strings.Split(strings.ToLower(path), ".")
	switch {
	case len(segments) == 1 && segments[0] == "active":
		if op == "remove" {
			changes.ActiveSet = true
			changes.Active = false
			return nil
		}
		value, err := decodeSCIMBool(raw)
		if err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
		}
		changes.ActiveSet = true
		changes.Active = value
	case len(segments) == 1 && segments[0] == "externalid":
		if op == "remove" {
			changes.ExternalIDSet = true
			changes.ExternalID = nil
			return nil
		}
		text, null, err := decodeSCIMNullableString(raw)
		if err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
		}
		changes.ExternalIDSet = true
		if null {
			changes.ExternalID = nil
		} else if err := validateSCIMExternalIDText(&text); err != nil {
			return err
		} else if text == "" {
			changes.ExternalID = nil
		} else {
			changes.ExternalID = &text
		}
	case len(segments) == 1 && segments[0] == "username":
		if op == "remove" {
			return newSCIMHandlerError(http.StatusConflict, "mutability", "userName cannot be changed through SCIM")
		}
		email, err := decodeSCIMString(raw)
		if err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
		}
		normalized, _, normalizeErr := scimUserName(&email)
		if normalizeErr != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", normalizeErr.Error())
		}
		if !strings.EqualFold(normalized, existing.Email) {
			return newSCIMHandlerError(http.StatusConflict, "mutability", "userName cannot be changed through SCIM")
		}
	case len(segments) == 1 && segments[0] == "emails":
		if op == "remove" {
			return newSCIMHandlerError(http.StatusConflict, "mutability", "emails cannot be changed through SCIM; they must match userName")
		}
		var emails []scimUserEmailPayload
		if err := json.Unmarshal(raw, &emails); err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "emails must be an array of email objects")
		}
		if err := validateSCIMEmails(emails, existing.Email); err != nil {
			return err
		}
	case segments[0] == "name" && len(segments) == 1:
		if op == "remove" {
			changes.Name = &localPart
			empty := ""
			changes.GivenName = &empty
			changes.FamilyName = &empty
			return nil
		}
		var name scimUserNamePayload
		if err := json.Unmarshal(raw, &name); err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "name must be an object")
		}
		formatted, given, family := scimDisplayName(&name, localPart)
		if err := validateSCIMNameText(formatted, given, family); err != nil {
			return err
		}
		changes.Name = &formatted
		changes.GivenName = &given
		changes.FamilyName = &family
	case segments[0] == "name" && len(segments) == 2:
		if op == "remove" {
			switch segments[1] {
			case "formatted":
				changes.Name = &localPart
			case "givenname":
				empty := ""
				changes.GivenName = &empty
			case "familyname":
				empty := ""
				changes.FamilyName = &empty
			default:
				return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
			}
			return nil
		}
		value, err := decodeSCIMString(raw)
		if err != nil {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
		}
		field := ""
		switch segments[1] {
		case "formatted":
			field = "name.formatted"
		case "givenname":
			field = "name.givenName"
		case "familyname":
			field = "name.familyName"
		default:
			return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
		}
		if err := validateSCIMText(field, value); err != nil {
			return err
		}
		switch segments[1] {
		case "formatted":
			if value == "" {
				value = localPart
			}
			changes.Name = &value
		case "givenname":
			changes.GivenName = &value
		case "familyname":
			changes.FamilyName = &value
		default:
			return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
		}
	default:
		return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
	}
	return nil
}

// scimUserQuery loads one tenant-scoped row.
func (s *Server) scimUserQuery(ctx context.Context, where string, args ...any) (scimUserRow, error) {
	return scanSCIMUser(s.store.DB.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE `+where, args...))
}

func (s *Server) scimUserByID(ctx context.Context, tenantID, id string) (scimUserRow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return scimUserRow{}, pgx.ErrNoRows
	}
	return s.scimUserQuery(ctx, `id = $1 AND tenant_id = $2`, id, tenantID)
}

func (s *Server) scimUserByEmail(ctx context.Context, tenantID, email string) (scimUserRow, error) {
	return s.scimUserQuery(ctx, `tenant_id = $1 AND email = $2`, tenantID, email)
}

func scimUserByIDTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (scimUserRow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return scimUserRow{}, pgx.ErrNoRows
	}
	return scanSCIMUser(tx.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// scimRequestIdentity resolves the authenticated tenant and public base URL.
func scimRequestIdentity(s *Server, w http.ResponseWriter, r *http.Request) (scimTenant, string, bool) {
	tenant, ok := scimTenantFromContext(r.Context())
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeSCIMError(w, http.StatusUnauthorized, "", "A valid SCIM bearer token is required")
		return scimTenant{}, "", false
	}
	base, err := s.scimBaseURL()
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "SCIM is not configured")
		return scimTenant{}, "", false
	}
	return tenant, base, true
}

// scimAudit records one SCIM write with a fixed action and no credential
// material. The request context has no web session, so this only appends to
// audit_log.
func (s *Server) scimAudit(r *http.Request, tenant scimTenant, action, target, detail string) {
	s.audit(r, userRecord{ID: "", TenantID: tenant.TenantID, Name: "SCIM token " + tenant.Name}, action, target, detail)
}

func (s *Server) scimAuditUserChange(r *http.Request, tenant scimTenant, email string, transition scimUserTransition) {
	for _, action := range scimUserAuditEvents(transition) {
		switch action {
		case "scim.user_updated":
			s.scimAudit(r, tenant, action, email, "Updated user through SCIM")
		case "scim.user_deactivated":
			s.scimAudit(r, tenant, action, email, "sessions revoked; mailbox preserved")
		case "scim.user_reactivated":
			s.scimAudit(r, tenant, action, email, "mailbox reactivated")
		}
	}
}

// registerSCIMUserRoutes mounts the Users collection and item endpoints behind
// bearer authentication. Both patterns are more specific than the /scim/v2/
// fallback, which stays registered last.
func (s *Server) registerSCIMUserRoutes(mux *http.ServeMux) {
	mux.Handle("/scim/v2/Users", s.scimAuth(http.HandlerFunc(s.scimUsersCollection)))
	mux.Handle("/scim/v2/Users/{id}", s.scimAuth(http.HandlerFunc(s.scimUsersItem)))
}

// scimUsersCollection dispatches the collection endpoint.
func (s *Server) scimUsersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.scimListUsers(w, r)
	case http.MethodPost:
		s.scimCreateUser(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeSCIMError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
	}
}

// scimUsersItem dispatches the per-resource endpoint.
func (s *Server) scimUsersItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.scimGetUser(w, r)
	case http.MethodPut:
		s.scimReplaceUser(w, r)
	case http.MethodPatch:
		s.scimPatchUser(w, r)
	case http.MethodDelete:
		s.scimDeleteUser(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeSCIMError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
	}
}

func (s *Server) scimListUsers(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	startIndex, count, err := parseSCIMPagination(r)
	if err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	where := "tenant_id = $1 AND deleted_at IS NULL"
	args := []any{tenant.TenantID}
	if rawFilter := strings.TrimSpace(r.URL.Query().Get("filter")); rawFilter != "" {
		field, value, filterErr := parseSCIMFilter(rawFilter, map[string]bool{"userName": true, "externalId": true})
		if filterErr != nil {
			writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "Only userName and externalId equality filters are supported")
			return
		}
		switch field {
		case "userName":
			args = append(args, strings.ToLower(strings.TrimSpace(value)))
			where += fmt.Sprintf(" AND email = $%d", len(args))
		case "externalId":
			args = append(args, strings.TrimSpace(value))
			where += fmt.Sprintf(" AND scim_external_id = $%d", len(args))
		default:
			writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "Only userName and externalId equality filters are supported")
			return
		}
	}
	ctx := r.Context()
	var total int
	if err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		s.logger.Warn("SCIM user list count failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM users")
		return
	}
	pageArgs := append(append([]any{}, args...), count, startIndex-1)
	rows, err := s.store.DB.Query(ctx, `SELECT `+scimUserColumns+` FROM users WHERE `+where+
		fmt.Sprintf(" ORDER BY email, id LIMIT $%d OFFSET $%d", len(pageArgs)-1, len(pageArgs)), pageArgs...)
	if err != nil {
		s.logger.Warn("SCIM user list failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM users")
		return
	}
	defer rows.Close()
	resources := []map[string]any{}
	for rows.Next() {
		row, scanErr := scanSCIMUser(rows)
		if scanErr != nil {
			s.logger.Warn("SCIM user list scan failed")
			writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM users")
			return
		}
		resources = append(resources, scimUserResource(row, base))
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("SCIM user list iteration failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM users")
		return
	}
	writeSCIMJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{scimListResponseSchema},
		"totalResults": total,
		"itemsPerPage": len(resources),
		"startIndex":   startIndex,
		"Resources":    resources,
	})
}

func (s *Server) scimGetUser(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	user, err := s.scimUserByID(r.Context(), tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
			return
		}
		s.logger.Warn("SCIM user lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not load the SCIM user")
		return
	}
	if user.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
		return
	}
	writeSCIMJSON(w, http.StatusOK, scimUserResource(user, base))
}

// scimCreateUser provisions a new user through the existing tenant
// provisioning path. SCIM never sets a usable password or any role other than
// member.
func (s *Server) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	var payload scimUserPayload
	if err := decodeSCIMRequest(w, r, &payload); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	email, localPart, err := scimUserName(payload.UserName)
	if err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	if payload.ExternalID.Set {
		if handlerErr := validateSCIMExternalIDText(payload.ExternalID.Value); handlerErr != nil {
			writeSCIMHandlerError(w, handlerErr)
			return
		}
	}
	externalID := normalizeSCIMExternalID(payload.ExternalID)
	name, given, family := scimDisplayName(payload.Name, localPart)
	if handlerErr := validateSCIMNameText(name, given, family); handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	active := payload.Active == nil || *payload.Active

	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not start user provisioning")
		return
	}
	defer tx.Rollback(ctx)

	existing, lookupErr := scanSCIMUser(tx.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE tenant_id = $1 AND email = $2 FOR UPDATE`, tenant.TenantID, email))
	if lookupErr == nil {
		switch classifySCIMCreate(&existing, externalID) {
		case scimCreateExisting:
			_ = tx.Rollback(ctx)
			w.Header().Set("Location", base+"/Users/"+existing.ID)
			writeSCIMJSON(w, http.StatusOK, scimUserResource(existing, base))
			return
		default:
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A user with this userName or externalId already exists")
			return
		}
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		s.logger.Warn("SCIM user lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	if externalID != nil {
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE tenant_id = $1 AND scim_external_id = $2)`, tenant.TenantID, *externalID).Scan(&conflict); err != nil {
			s.logger.Warn("SCIM external id lookup failed")
			writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
			return
		}
		if conflict {
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A user with this userName or externalId already exists")
			return
		}
	}
	if err := ensureTenantDomain(ctx, tx, tenant.TenantID, email); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", "userName must use a verified domain for this organization")
		return
	}
	webHash, err := auth.HashPassword(randomToken(32))
	if err != nil {
		s.logger.Warn("SCIM password hash failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	mailHash, err := hashMailPassword(randomToken(32))
	if err != nil {
		s.logger.Warn("SCIM mail password hash failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	status := "active"
	if !active {
		status = "suspended"
	}
	userID, err := provisionUserTx(ctx, tx, tenant.TenantID, name, email, "member", webHash, mailHash,
		scimUserProvisionQuota, "", "", "", "", "", nil, status, true, s.cfg.MaildirBase)
	if err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			// A concurrent create may have won the race. Re-read the pair and
			// answer 200 only when it is an exact replay.
			if replayed, replayErr := s.scimUserByEmail(ctx, tenant.TenantID, email); replayErr == nil &&
				classifySCIMCreate(&replayed, externalID) == scimCreateExisting {
				w.Header().Set("Location", base+"/Users/"+replayed.ID)
				writeSCIMJSON(w, http.StatusOK, scimUserResource(replayed, base))
				return
			}
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A user with this userName or externalId already exists")
			return
		}
		s.logger.Warn("SCIM user provisioning failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users
		SET given_name = $3, family_name = $4, scim_external_id = $5,
		    role = 'member', role_source = 'manual', updated_at = now()
		WHERE id = $1 AND tenant_id = $2
	`, userID, tenant.TenantID, given, family, externalID); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			// A concurrent create claimed the same externalId between the
			// pre-check and this update. The whole provisioning transaction
			// rolled back, so there is no partial user.
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A user with this userName or externalId already exists")
			return
		}
		s.logger.Warn("SCIM user attribute update failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	created, err := scanSCIMUser(tx.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE id = $1 AND tenant_id = $2`, userID, tenant.TenantID))
	if err != nil {
		s.logger.Warn("SCIM user reload failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM user")
		return
	}
	// app_states powers bootstrap/onboarding and can only be built for an
	// active user, so it is initialized after commit (mirroring admin create).
	if created.Status == "active" {
		s.scimEnsureUserState(ctx, created.Email)
	}
	s.scimAudit(r, tenant, "scim.user_created", email, "Created user through SCIM")
	w.Header().Set("Location", base+"/Users/"+created.ID)
	writeSCIMJSON(w, http.StatusCreated, scimUserResource(created, base))
}

// scimEnsureUserState initializes the per-user application state. A failure is
// logged but never fails provisioning, matching adminCreateUser.
func (s *Server) scimEnsureUserState(ctx context.Context, email string) {
	if stateErr := s.ensureUserState(ctx, email); stateErr != nil {
		s.logger.Warn("SCIM user state provisioning failed", "email", email, "err", stateErr)
	}
}

// revokeSCIMUserAccess clears every live authentication artefact for a
// deprovisioned user. Web/admin sessions and pending challenges are deleted
// together so an in-flight login cannot complete later; registered passkeys are
// kept because the account may be reactivated.
func revokeSCIMUserAccess(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	statements := []struct {
		sql  string
		args []any
	}{
		{sql: `DELETE FROM sessions WHERE tenant_id = $1 AND user_id = $2`, args: []any{tenantID, userID}},
		{sql: `DELETE FROM auth_challenges WHERE tenant_id = $1 AND user_id = $2`, args: []any{tenantID, userID}},
		{sql: `DELETE FROM admin_sessions WHERE tenant_id = $1 AND user_id = $2`, args: []any{tenantID, userID}},
		{sql: `DELETE FROM trusted_devices WHERE user_id = $1`, args: []any{userID}},
		{sql: `DELETE FROM admin_auth_challenges WHERE tenant_id = $1 AND user_id = $2`, args: []any{tenantID, userID}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

// scimApplyUserChanges persists a validated mutation inside the caller's
// transaction. It owns the owner-protection and externalId-conflict checks so
// PUT and PATCH cannot diverge.
func (s *Server) scimApplyUserChanges(ctx context.Context, tx pgx.Tx, tenantID string, existing scimUserRow, changes scimUserChanges) (scimUserRow, scimUserTransition, *scimHandlerError) {
	var transition scimUserTransition
	if changes.ExternalIDSet && changes.ExternalID != nil {
		var conflict bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM users WHERE tenant_id = $1 AND scim_external_id = $2 AND id <> $3)
		`, tenantID, *changes.ExternalID, existing.ID).Scan(&conflict); err != nil {
			s.logger.Warn("SCIM external id conflict check failed")
			return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
		}
		if conflict {
			return existing, transition, newSCIMHandlerError(http.StatusConflict, "uniqueness", "externalId is already in use")
		}
	}
	// Owners are managed manually only. An already-suspended owner can still
	// receive an idempotent active=false, but a live owner cannot be locked out
	// through SCIM.
	if changes.ActiveSet && !changes.Active && existing.Role == "owner" && existing.Status == "active" {
		return existing, transition, newSCIMHandlerError(http.StatusConflict, "mutability", "Owner accounts cannot be deactivated through SCIM")
	}
	sets := []string{}
	args := []any{existing.ID, tenantID}
	if changes.Name != nil {
		args = append(args, *changes.Name)
		sets = append(sets, fmt.Sprintf("name = $%d", len(args)))
	}
	if changes.GivenName != nil {
		args = append(args, *changes.GivenName)
		sets = append(sets, fmt.Sprintf("given_name = $%d", len(args)))
	}
	if changes.FamilyName != nil {
		args = append(args, *changes.FamilyName)
		sets = append(sets, fmt.Sprintf("family_name = $%d", len(args)))
	}
	if changes.ExternalIDSet {
		args = append(args, changes.ExternalID)
		sets = append(sets, fmt.Sprintf("scim_external_id = $%d", len(args)))
	}
	if changes.ActiveSet {
		status := "active"
		if !changes.Active {
			status = "suspended"
		}
		args = append(args, status)
		sets = append(sets, fmt.Sprintf("status = $%d", len(args)))
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = now()")
		if _, err := tx.Exec(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = $1 AND tenant_id = $2`, args...); err != nil {
			if isUniqueViolation(err) {
				// A concurrent PUT/PATCH claimed this externalId between the
				// pre-check and the update.
				return existing, transition, newSCIMHandlerError(http.StatusConflict, "uniqueness", "externalId is already in use")
			}
			s.logger.Warn("SCIM user update failed")
			return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
		}
	}
	if changes.ActiveSet {
		if !changes.Active {
			// Deactivation is enforced on every request, even when the user is
			// already suspended, so a retry cannot leave a live session behind.
			// The audit fires only for the actual transition.
			transition.Deactivated = existing.Status != "suspended"
			if _, err := tx.Exec(ctx, `UPDATE mailboxes SET active = false WHERE tenant_id = $1 AND user_id = $2`, tenantID, existing.ID); err != nil {
				s.logger.Warn("SCIM mailbox deactivation failed")
				return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
			}
			if err := revokeSCIMUserAccess(ctx, tx, tenantID, existing.ID); err != nil {
				s.logger.Warn("SCIM session revocation failed")
				return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
			}
		} else {
			transition.Reactivated = existing.Status != "active"
			if _, err := tx.Exec(ctx, `UPDATE mailboxes SET active = true WHERE tenant_id = $1 AND user_id = $2`, tenantID, existing.ID); err != nil {
				s.logger.Warn("SCIM mailbox reactivation failed")
				return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
			}
		}
	}
	transition.MetadataChanged = changes.metadataChanged(existing)
	updated, err := scanSCIMUser(tx.QueryRow(ctx, `SELECT `+scimUserColumns+` FROM users WHERE id = $1 AND tenant_id = $2`, existing.ID, tenantID))
	if err != nil {
		s.logger.Warn("SCIM user reload failed")
		return existing, transition, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM user")
	}
	return updated, transition, nil
}

func (s *Server) scimReplaceUser(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	var payload scimUserPayload
	if err := decodeSCIMRequest(w, r, &payload); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimUserByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
			return
		}
		s.logger.Warn("SCIM user lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	if existing.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
		return
	}
	changes, handlerErr := scimReplaceChanges(payload, existing)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	updated, transition, handlerErr := s.scimApplyUserChanges(ctx, tx, tenant.TenantID, existing, changes)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	if transition.Reactivated {
		// A user created suspended has no app_states yet; initialize it once
		// reactivation has been committed so bootstrap/onboarding works.
		s.scimEnsureUserState(ctx, updated.Email)
	}
	s.scimAuditUserChange(r, tenant, updated.Email, transition)
	writeSCIMJSON(w, http.StatusOK, scimUserResource(updated, base))
}

func (s *Server) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	var payload scimPatchRequest
	if err := decodeSCIMRequest(w, r, &payload); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimUserByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
			return
		}
		s.logger.Warn("SCIM user lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	if existing.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
		return
	}
	changes, handlerErr := scimPatchChanges(payload, existing)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	updated, transition, handlerErr := s.scimApplyUserChanges(ctx, tx, tenant.TenantID, existing, changes)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM user")
		return
	}
	if transition.Reactivated {
		// A user created suspended has no app_states yet; initialize it once
		// reactivation has been committed so bootstrap/onboarding works.
		s.scimEnsureUserState(ctx, updated.Email)
	}
	s.scimAuditUserChange(r, tenant, updated.Email, transition)
	writeSCIMJSON(w, http.StatusOK, scimUserResource(updated, base))
}

// scimDeleteUser deprovisions a user without deleting the row or any mail.
func (s *Server) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	tenant, _, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimUserByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
			return
		}
		s.logger.Warn("SCIM user lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	if existing.DeletedAt != nil {
		writeSCIMError(w, http.StatusNotFound, "", "SCIM user not found")
		return
	}
	if existing.Role == "owner" && existing.Status == "active" {
		writeSCIMError(w, http.StatusConflict, "mutability", "Owner accounts cannot be deactivated through SCIM")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET status = 'suspended', updated_at = now() WHERE id = $1 AND tenant_id = $2`, existing.ID, tenant.TenantID); err != nil {
		s.logger.Warn("SCIM user deactivation failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE mailboxes SET active = false WHERE tenant_id = $1 AND user_id = $2`, tenant.TenantID, existing.ID); err != nil {
		s.logger.Warn("SCIM mailbox deactivation failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	if err := revokeSCIMUserAccess(ctx, tx, tenant.TenantID, existing.ID); err != nil {
		s.logger.Warn("SCIM session revocation failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM user")
		return
	}
	s.scimAudit(r, tenant, "scim.user_deleted", existing.Email, "deactivated; sessions revoked; mailbox preserved")
	w.WriteHeader(http.StatusNoContent)
}
