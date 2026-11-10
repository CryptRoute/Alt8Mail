package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// scimGroupNameMaxLength matches the admin group-name cap. A longer name is
	// rejected instead of silently truncated so IdP matching stays predictable.
	scimGroupNameMaxLength = 100
)

// scimGroupColumns is the projection shared by every Groups query.
const scimGroupColumns = `id::text, name, email::text, description, scim_external_id, scim_role,
	group_type, status, created_at, updated_at`

// scimGroupRow is an internal row. It is never marshalled directly; the SCIM
// resource shape is built by scimGroupResource.
type scimGroupRow struct {
	ID          string
	Name        string
	Email       string
	Description string
	ExternalID  *string
	ScimRole    string
	GroupType   string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// scimGroupMember is the non-secret projection of a group member used when
// rendering the SCIM Group resource.
type scimGroupMember struct {
	Value   string
	Display string
}

// scimGroupPayload is the subset of the RFC 7643 Group resource this server
// understands. The local scim_role mapping is deliberately not a SCIM attribute.
type scimGroupPayload struct {
	Schemas     []string                 `json:"schemas"`
	ID          string                   `json:"id"`
	ExternalID  scimNullableString       `json:"externalId"`
	DisplayName *string                  `json:"displayName"`
	Members     []scimGroupMemberPayload `json:"members"`
	Meta        json.RawMessage          `json:"meta"`
}

type scimGroupMemberPayload struct {
	Value   string `json:"value"`
	Display string `json:"display"`
	Ref     string `json:"$ref"`
}

// scimGroupChangeSet is the validated, not-yet-persisted mutation produced by
// PUT and PATCH. MemberIDs is the desired complete membership when MembersSet is
// true; LiveMemberIDs lists the ids introduced by the write (PUT members, or
// PATCH add/replace) that must resolve to live users. Unchanged members that are
// closed accounts stay in MemberIDs but are not re-validated. ReferencedIDs
// lists every member id mentioned by the request so unknown and cross-tenant ids
// are rejected even for idempotent removals.
type scimGroupChangeSet struct {
	DisplayNameSet bool
	DisplayName    string
	ExternalIDSet  bool
	ExternalID     *string
	MembersSet     bool
	MemberIDs      []string
	ReferencedIDs  []string
	LiveMemberIDs  []string
}

// scimRoleChange records one user whose role was recomputed from group
// mappings. NewRole is empty when no mapped role remained and the role was
// reset to the default member role.
type scimRoleChange struct {
	UserID      string
	Email       string
	OldRole     string
	NewRole     string
	RoleChanged bool
}

// scimGroupQueryer is satisfied by both pgxpool.Pool and pgx.Tx.
type scimGroupQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// scimRolePrecedence ranks the local mapping values. Anything else (including
// the empty string and the never-mappable owner role) ranks zero.
func scimRolePrecedence(role string) int {
	switch role {
	case "admin":
		return 3
	case "member":
		return 2
	case "readonly":
		return 1
	}
	return 0
}

// scimHighestMappedRole returns the highest-precedence mapped role, or "" when
// the user has no active mapped group.
func scimHighestMappedRole(roles []string) string {
	best := ""
	for _, role := range roles {
		if scimRolePrecedence(role) > scimRolePrecedence(best) {
			best = role
		}
	}
	return best
}

// uniqueStrings preserves first-seen order and drops duplicates. SCIM member
// lists are sets; a duplicate add is idempotent.
func scimUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func scimStringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// scimMembershipDiff returns the ids added to and removed from the group.
func scimMembershipDiff(oldIDs, newIDs []string) (added, removed []string) {
	oldSet := scimStringSet(oldIDs)
	newSet := scimStringSet(newIDs)
	for _, id := range newIDs {
		if !oldSet[id] {
			added = append(added, id)
		}
	}
	for _, id := range oldIDs {
		if !newSet[id] {
			removed = append(removed, id)
		}
	}
	return scimUniqueStrings(added), scimUniqueStrings(removed)
}

// normalizeSCIMGroupDisplayName validates the required displayName attribute.
func normalizeSCIMGroupDisplayName(raw *string) (string, *scimHandlerError) {
	if raw == nil {
		return "", newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "displayName is required")
	}
	name := strings.TrimSpace(*raw)
	if name == "" {
		return "", newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "displayName is required")
	}
	if scimHasControlCharacters(name) {
		return "", newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "displayName must not contain control characters")
	}
	if len([]rune(name)) > scimGroupNameMaxLength {
		return "", newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "displayName must be 100 characters or fewer")
	}
	return name, nil
}

// decodeSCIMGroupMemberValues accepts a single member object or an array of
// member objects and returns the canonical, de-duplicated user id of each.
// Canonicalizing here is essential: membership is a set operation, so an
// uppercase spelling must still add/remove the canonical row.
func decodeSCIMGroupMemberValues(raw json.RawMessage) ([]string, *scimHandlerError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value must be an object or an array of objects")
	}
	items := []json.RawMessage{trimmed}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value must be an object or an array of objects")
		}
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		var member scimGroupMemberPayload
		if err := json.Unmarshal(item, &member); err != nil {
			return nil, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members entries must be objects with a value")
		}
		value := strings.TrimSpace(member.Value)
		if value == "" {
			return nil, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value is required")
		}
		ids = append(ids, value)
	}
	return scimCanonicalMemberIDs(ids)
}

// scimGroupVersion is a weak ETag derived from the row's last modification
// time, matching the Users resource convention.
func scimGroupVersion(row scimGroupRow) string {
	return `W/"` + fmt.Sprintf("%x", row.UpdatedAt.UTC().UnixMicro()) + `"`
}

// scimGroupResource renders the RFC 7643 core Group shape. externalId is
// omitted when SQL NULL; the local role mapping is never exposed as a SCIM
// attribute.
func scimGroupResource(group scimGroupRow, members []scimGroupMember, base string) map[string]any {
	rendered := make([]map[string]any, 0, len(members))
	for _, member := range members {
		rendered = append(rendered, map[string]any{
			"value":   member.Value,
			"display": member.Display,
			"$ref":    base + "/Users/" + member.Value,
		})
	}
	resource := map[string]any{
		"schemas":     []string{scimGroupSchema},
		"id":          group.ID,
		"displayName": group.Name,
		"members":     rendered,
		"meta": map[string]any{
			"resourceType": "Group",
			"created":      group.CreatedAt.UTC().Format(time.RFC3339),
			"lastModified": group.UpdatedAt.UTC().Format(time.RFC3339),
			"location":     base + "/Groups/" + group.ID,
			"version":      scimGroupVersion(group),
		},
	}
	if group.ExternalID != nil {
		resource["externalId"] = *group.ExternalID
	}
	return resource
}

func scanSCIMGroup(row pgx.Row) (scimGroupRow, error) {
	var group scimGroupRow
	err := row.Scan(&group.ID, &group.Name, &group.Email, &group.Description, &group.ExternalID, &group.ScimRole,
		&group.GroupType, &group.Status, &group.CreatedAt, &group.UpdatedAt)
	return group, err
}

// scimGroupByID loads one tenant-scoped group. Malformed ids are reported as
// not found so they can never reach PostgreSQL.
func (s *Server) scimGroupByID(ctx context.Context, tenantID, id string) (scimGroupRow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return scimGroupRow{}, pgx.ErrNoRows
	}
	return scanSCIMGroup(s.store.DB.QueryRow(ctx,
		`SELECT `+scimGroupColumns+` FROM groups WHERE id = $1 AND tenant_id = $2`, id, tenantID))
}

func scimGroupByIDTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (scimGroupRow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return scimGroupRow{}, pgx.ErrNoRows
	}
	return scanSCIMGroup(tx.QueryRow(ctx,
		`SELECT `+scimGroupColumns+` FROM groups WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
}

// scimGroupMembersFor loads a single group's members in deterministic order.
func scimGroupMembersFor(ctx context.Context, q scimGroupQueryer, tenantID, groupID string) ([]scimGroupMember, error) {
	rows, err := q.Query(ctx, `
		SELECT u.id::text, CASE WHEN u.name <> '' THEN u.name ELSE u.email::text END
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		JOIN users u ON u.id = gm.user_id AND u.tenant_id = g.tenant_id
		WHERE gm.group_id = $1 AND g.tenant_id = $2 AND u.deleted_at IS NULL
		ORDER BY u.name, u.id
	`, groupID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []scimGroupMember{}
	for rows.Next() {
		var member scimGroupMember
		if err := rows.Scan(&member.Value, &member.Display); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// scimGroupMembersByGroup loads the members of every group in ids with two
// queries at most (one when the page is empty).
func scimGroupMembersByGroup(ctx context.Context, q scimGroupQueryer, tenantID string, groupIDs []string) (map[string][]scimGroupMember, error) {
	members := make(map[string][]scimGroupMember, len(groupIDs))
	if len(groupIDs) == 0 {
		return members, nil
	}
	placeholders := make([]string, len(groupIDs))
	args := make([]any, 0, len(groupIDs)+1)
	args = append(args, tenantID)
	for index, id := range groupIDs {
		args = append(args, id)
		placeholders[index] = fmt.Sprintf("$%d", index+2)
	}
	rows, err := q.Query(ctx, `
		SELECT gm.group_id::text, u.id::text, CASE WHEN u.name <> '' THEN u.name ELSE u.email::text END
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		JOIN users u ON u.id = gm.user_id AND u.tenant_id = g.tenant_id
		WHERE g.tenant_id = $1 AND gm.group_id IN (`+strings.Join(placeholders, ", ")+`)
		  AND u.deleted_at IS NULL
		ORDER BY gm.group_id, u.name, u.id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var groupID string
		var member scimGroupMember
		if err := rows.Scan(&groupID, &member.Value, &member.Display); err != nil {
			return nil, err
		}
		members[groupID] = append(members[groupID], member)
	}
	return members, rows.Err()
}

// scimCanonicalMemberIDs validates UUIDs and returns their canonical lowercase
// form, dropping duplicates. Malformed values are a SCIM invalidValue.
func scimCanonicalMemberIDs(ids []string) ([]string, *scimHandlerError) {
	canonical := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		parsed, err := uuid.Parse(strings.TrimSpace(id))
		if err != nil {
			return nil, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value must be a user id in this organization")
		}
		value := parsed.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		canonical = append(canonical, value)
	}
	return canonical, nil
}

func scimUUIDArray(ids []string) []pgtype.UUID {
	values := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		values = append(values, pgtype.UUID{Bytes: [16]byte(parsed), Valid: true})
	}
	return values
}

// scimValidateGroupMemberIDs rejects ids that do not belong to a live user in
// the token's tenant. Cross-tenant ids are indistinguishable from unknown ids on
// purpose. Used for the final membership of a write.
func scimValidateGroupMemberIDs(ctx context.Context, q scimGroupQueryer, tenantID string, ids []string) *scimHandlerError {
	canonical, handlerErr := scimCanonicalMemberIDs(ids)
	if handlerErr != nil {
		return handlerErr
	}
	return scimValidateMemberIDsExist(ctx, q, tenantID, canonical, true)
}

// scimValidateReferencedGroupMemberIDs checks every member id mentioned by a
// request against tenant users, including closed accounts so an idempotent
// removal of a soft-deleted member still succeeds. Add/replace targets are
// additionally validated as live users by scimValidateGroupMemberIDs.
func scimValidateReferencedGroupMemberIDs(ctx context.Context, q scimGroupQueryer, tenantID string, ids []string) *scimHandlerError {
	canonical, handlerErr := scimCanonicalMemberIDs(ids)
	if handlerErr != nil {
		return handlerErr
	}
	return scimValidateMemberIDsExist(ctx, q, tenantID, canonical, false)
}

func scimValidateMemberIDsExist(ctx context.Context, q scimGroupQueryer, tenantID string, ids []string, liveOnly bool) *scimHandlerError {
	if len(ids) == 0 {
		return nil
	}
	query := `SELECT id::text FROM users WHERE tenant_id = $1 AND id = ANY($2::uuid[])`
	if liveOnly {
		query += ` AND deleted_at IS NULL`
	}
	rows, err := q.Query(ctx, query, tenantID, scimUUIDArray(ids))
	if err != nil {
		return newSCIMHandlerError(http.StatusInternalServerError, "", "Could not validate group members")
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return newSCIMHandlerError(http.StatusInternalServerError, "", "Could not validate group members")
		}
		found[id] = true
	}
	if err := rows.Err(); err != nil {
		return newSCIMHandlerError(http.StatusInternalServerError, "", "Could not validate group members")
	}
	for _, id := range ids {
		if !found[id] {
			return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value must be a user id in this organization")
		}
	}
	return nil
}

// scimGroupDomainTx picks the tenant's primary verified/manual domain. SCIM
// never creates or verifies a domain.
func scimGroupDomainTx(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var domain string
	err := tx.QueryRow(ctx, `
		SELECT domain::text FROM mail_domains
		WHERE tenant_id = $1 AND status IN ('verified', 'manual')
		ORDER BY is_primary DESC, created_at, domain
		LIMIT 1
	`, tenantID).Scan(&domain)
	return domain, err
}

// scimGroupInternalEmail synthesizes the collision-resistant internal mailbox
// address the groups table requires. The address is server-generated and never
// exposed as a SCIM attribute.
func scimGroupInternalEmail(domain string) string {
	return "scim-" + uuid.NewString() + "@" + strings.ToLower(strings.TrimSpace(domain))
}

// scimRecomputeUserRoleTx is the interface consumed by the membership writers:
// it returns the old and new role only when the role actually changed. The
// richer internal form below also reports the email and whether the change came
// from a group mapping so callers can pick scim.role_mapped or
// scim.role_unmapped.
func (s *Server) scimRecomputeUserRoleTx(ctx context.Context, tx pgx.Tx, tenantID, userID string) (oldRole, newRole string, err error) {
	change, err := s.scimRecomputeUserRole(ctx, tx, tenantID, userID)
	if err != nil || !change.RoleChanged {
		return "", "", err
	}
	return change.OldRole, change.NewRole, nil
}

// scimRecomputeUserRole applies the local group→role mapping to one user inside
// the caller's transaction.
//
//   - Owners are never touched, so SCIM can neither grant nor demote owner.
//   - The highest mapped scim_role among the user's active groups wins
//     (admin > member > readonly) and is applied with role_source='scim'.
//   - With no mapped role, a user previously managed by mapping (role_source=
//     'scim') is reset to member/'manual'.
//   - A manually assigned role (role_source='manual') is never overridden by the
//     absence of a mapping, so an explicit admin decision survives a recompute.
func (s *Server) scimRecomputeUserRole(ctx context.Context, tx pgx.Tx, tenantID, userID string) (scimRoleChange, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return scimRoleChange{}, nil
	}
	var email, role, roleSource string
	var deletedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT email::text, role, role_source, deleted_at
		FROM users WHERE id = $1 AND tenant_id = $2
		FOR UPDATE
	`, userID, tenantID).Scan(&email, &role, &roleSource, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return scimRoleChange{}, nil
	}
	if err != nil {
		return scimRoleChange{}, err
	}
	change := scimRoleChange{UserID: userID, Email: email, OldRole: role}
	if deletedAt != nil || role == "owner" {
		return change, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT g.scim_role
		FROM group_members gm
		JOIN groups g ON g.id = gm.group_id
		WHERE gm.user_id = $1 AND g.tenant_id = $2
		  AND g.status = 'active' AND g.group_type = 'security' AND g.scim_role <> ''
	`, userID, tenantID)
	if err != nil {
		return change, err
	}
	mappedRoles := []string{}
	for rows.Next() {
		var mapped string
		if err := rows.Scan(&mapped); err != nil {
			rows.Close()
			return change, err
		}
		mappedRoles = append(mappedRoles, mapped)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return change, err
	}
	mapped := scimHighestMappedRole(mappedRoles)

	desiredRole, desiredSource := "", ""
	switch {
	case mapped != "":
		desiredRole, desiredSource = mapped, "scim"
	case roleSource == "scim":
		desiredRole, desiredSource = "member", "manual"
	default:
		// Explicit manual role; nothing to do.
		change.NewRole = role
		return change, nil
	}
	if role == desiredRole && roleSource == desiredSource {
		change.NewRole = mapped
		return change, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET role = $3, role_source = $4, updated_at = now()
		WHERE id = $1 AND tenant_id = $2
	`, userID, tenantID, desiredRole, desiredSource); err != nil {
		return change, err
	}
	change.NewRole = mapped // "" when the mapping was removed
	change.RoleChanged = role != desiredRole
	if !change.RoleChanged {
		change.NewRole = desiredRole
	}
	return change, nil
}

// scimRecomputeRolesTx recomputes a set of users in deterministic id order so
// concurrent membership writers always take user locks in the same sequence.
func (s *Server) scimRecomputeRolesTx(ctx context.Context, tx pgx.Tx, tenantID string, userIDs []string) ([]scimRoleChange, error) {
	ids := scimUniqueStrings(userIDs)
	sort.Strings(ids)
	changes := []scimRoleChange{}
	for _, id := range ids {
		change, err := s.scimRecomputeUserRole(ctx, tx, tenantID, id)
		if err != nil {
			return nil, err
		}
		if change.RoleChanged {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

// scimRoleAuditDetail describes one recompute result. mapped is false when the
// mapping was removed and the role was reset to member.
func scimRoleAuditDetail(change scimRoleChange) (mapped bool, detail string) {
	if change.NewRole == "" {
		return false, fmt.Sprintf("no mapped role; role %s reset to member", change.OldRole)
	}
	return true, fmt.Sprintf("role %s mapped to %s", change.OldRole, change.NewRole)
}

// scimGroupRoleAuditAction maps one recompute result to the SCIM-origin audit
// action.
func scimGroupRoleAuditAction(change scimRoleChange) (action, detail string) {
	mapped, detail := scimRoleAuditDetail(change)
	if mapped {
		return "scim.role_mapped", detail
	}
	return "scim.role_unmapped", detail
}

// adminGroupRoleAuditAction maps one recompute result to the admin-origin audit
// action so SIEM filters can distinguish IdP-driven changes from console ones.
func adminGroupRoleAuditAction(change scimRoleChange) (action, detail string) {
	mapped, detail := scimRoleAuditDetail(change)
	if mapped {
		return "admin.role_mapped", detail
	}
	return "admin.role_unmapped", detail
}

// scimAuditRoleChanges records mapped/unmapped transitions for SCIM-originated
// membership writes.
func (s *Server) scimAuditRoleChanges(r *http.Request, tenant scimTenant, changes []scimRoleChange) {
	for _, change := range changes {
		action, detail := scimGroupRoleAuditAction(change)
		s.scimAudit(r, tenant, action, change.Email, detail)
	}
}

// adminAuditRoleChanges records the same transitions for admin-originated
// membership writes in the admin audit log.
func (s *Server) adminAuditRoleChanges(ctx context.Context, sess *adminSessionRecord, changes []scimRoleChange) {
	for _, change := range changes {
		action, detail := adminGroupRoleAuditAction(change)
		newRole := change.NewRole
		if newRole == "" {
			newRole = "member"
		}
		s.recordAdminAudit(ctx, sess, sess.User, action, change.Email, detail, map[string]any{
			"userId": change.UserID, "oldRole": change.OldRole, "newRole": newRole,
		})
	}
}

// scimGroupReplaceChanges validates a PUT payload. Omitted attributes are left
// unchanged; an explicit members array replaces the complete membership.
func scimGroupReplaceChanges(payload scimGroupPayload) (scimGroupChangeSet, *scimHandlerError) {
	var changes scimGroupChangeSet
	if payload.DisplayName != nil {
		name, handlerErr := normalizeSCIMGroupDisplayName(payload.DisplayName)
		if handlerErr != nil {
			return changes, handlerErr
		}
		changes.DisplayNameSet = true
		changes.DisplayName = name
	}
	if payload.ExternalID.Set {
		if handlerErr := validateSCIMExternalIDText(payload.ExternalID.Value); handlerErr != nil {
			return changes, handlerErr
		}
		changes.ExternalIDSet = true
		changes.ExternalID = normalizeSCIMExternalID(payload.ExternalID)
	}
	if payload.Members != nil {
		ids := make([]string, 0, len(payload.Members))
		for _, member := range payload.Members {
			value := strings.TrimSpace(member.Value)
			if value == "" {
				return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "members value is required")
			}
			ids = append(ids, value)
		}
		canonical, handlerErr := scimCanonicalMemberIDs(ids)
		if handlerErr != nil {
			return changes, handlerErr
		}
		changes.MembersSet = true
		changes.MemberIDs = canonical
		changes.ReferencedIDs = canonical
		changes.LiveMemberIDs = canonical
	}
	return changes, nil
}

// scimGroupPatchChanges is the pure PATCH parser. Membership operations are
// applied in order against the current membership so add/remove/replace can be
// combined and idempotent no-ops are natural.
func scimGroupPatchChanges(payload scimPatchRequest, current []string) (scimGroupChangeSet, *scimHandlerError) {
	var changes scimGroupChangeSet
	if len(payload.Operations) == 0 {
		return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH requires at least one operation")
	}
	members := append([]string(nil), current...)
	referenced := []string{}
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
			if handlerErr := applySCIMGroupPatchObject(&changes, &members, &referenced, operation.Value); handlerErr != nil {
				return changes, handlerErr
			}
			continue
		}
		switch strings.ToLower(path) {
		case "displayname":
			if op == "remove" {
				return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "displayName is required and cannot be removed")
			}
			text, err := decodeSCIMString(operation.Value)
			if err != nil {
				return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			name, handlerErr := normalizeSCIMGroupDisplayName(&text)
			if handlerErr != nil {
				return changes, handlerErr
			}
			changes.DisplayNameSet = true
			changes.DisplayName = name
		case "externalid":
			if op == "remove" {
				changes.ExternalIDSet = true
				changes.ExternalID = nil
				continue
			}
			text, null, err := decodeSCIMNullableString(operation.Value)
			if err != nil {
				return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			changes.ExternalIDSet = true
			if null || text == "" {
				changes.ExternalID = nil
				continue
			}
			if handlerErr := validateSCIMExternalIDText(&text); handlerErr != nil {
				return changes, handlerErr
			}
			changes.ExternalID = &text
		case "members":
			ids, handlerErr := decodeSCIMGroupMemberValues(operation.Value)
			if handlerErr != nil {
				return changes, handlerErr
			}
			referenced = append(referenced, ids...)
			switch op {
			case "add":
				present := scimStringSet(members)
				for _, id := range ids {
					if !present[id] {
						members = append(members, id)
						present[id] = true
					}
				}
				changes.LiveMemberIDs = append(changes.LiveMemberIDs, ids...)
			case "remove":
				removed := scimStringSet(ids)
				remaining := members[:0]
				for _, id := range members {
					if !removed[id] {
						remaining = append(remaining, id)
					}
				}
				members = remaining
			case "replace":
				members = scimUniqueStrings(ids)
				changes.LiveMemberIDs = append(changes.LiveMemberIDs, ids...)
			}
			changes.MembersSet = true
		default:
			return changes, newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
		}
	}
	changes.MemberIDs = scimUniqueStrings(members)
	changes.ReferencedIDs = scimUniqueStrings(referenced)
	changes.LiveMemberIDs = scimUniqueStrings(changes.LiveMemberIDs)
	return changes, nil
}

func applySCIMGroupPatchObject(changes *scimGroupChangeSet, members *[]string, referenced *[]string, raw json.RawMessage) *scimHandlerError {
	if len(bytes.TrimSpace(raw)) == 0 {
		return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH replace requires a value")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", "PATCH replace value must be an object")
	}
	for key, value := range object {
		switch strings.ToLower(key) {
		case "displayname":
			text, err := decodeSCIMString(value)
			if err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			name, handlerErr := normalizeSCIMGroupDisplayName(&text)
			if handlerErr != nil {
				return handlerErr
			}
			changes.DisplayNameSet = true
			changes.DisplayName = name
		case "externalid":
			text, null, err := decodeSCIMNullableString(value)
			if err != nil {
				return newSCIMHandlerError(http.StatusBadRequest, "invalidValue", err.Error())
			}
			changes.ExternalIDSet = true
			if null || text == "" {
				changes.ExternalID = nil
				continue
			}
			if handlerErr := validateSCIMExternalIDText(&text); handlerErr != nil {
				return handlerErr
			}
			changes.ExternalID = &text
		case "members":
			ids, handlerErr := decodeSCIMGroupMemberValues(value)
			if handlerErr != nil {
				return handlerErr
			}
			*referenced = append(*referenced, ids...)
			*members = scimUniqueStrings(ids)
			changes.LiveMemberIDs = append(changes.LiveMemberIDs, ids...)
			changes.MembersSet = true
		case "schemas", "id", "meta":
			// Read-only resource envelope attributes are ignored when echoed.
		default:
			return newSCIMHandlerError(http.StatusBadRequest, "invalidPath", "PATCH path is not supported")
		}
	}
	return nil
}

// scimApplyGroupChanges persists a validated mutation inside the caller's
// transaction and recomputes every affected member's role. It reports whether
// any persisted attribute or membership actually changed.
func (s *Server) scimApplyGroupChanges(ctx context.Context, tx pgx.Tx, tenantID string, existing scimGroupRow, changes scimGroupChangeSet) (scimGroupRow, bool, []scimRoleChange, *scimHandlerError) {
	// Every id the request mentions must belong to a tenant user. Ids
	// introduced by PUT/add/replace must additionally be live users; unchanged
	// members that are closed accounts are allowed to remain so an unrelated
	// membership edit does not fail the whole request. A membership-only PATCH
	// never validates unchanged member ids.
	if handlerErr := scimValidateReferencedGroupMemberIDs(ctx, tx, tenantID, changes.ReferencedIDs); handlerErr != nil {
		return existing, false, nil, handlerErr
	}
	if handlerErr := scimValidateGroupMemberIDs(ctx, tx, tenantID, changes.LiveMemberIDs); handlerErr != nil {
		return existing, false, nil, handlerErr
	}
	if changes.ExternalIDSet && changes.ExternalID != nil && !sameSCIMExternalID(changes.ExternalID, existing.ExternalID) {
		var conflict bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM groups WHERE tenant_id = $1 AND scim_external_id = $2 AND id <> $3)
		`, tenantID, *changes.ExternalID, existing.ID).Scan(&conflict); err != nil {
			s.logger.Warn("SCIM group external id conflict check failed")
			return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
		}
		if conflict {
			return existing, false, nil, newSCIMHandlerError(http.StatusConflict, "uniqueness", "externalId is already in use")
		}
	}
	setParts := []string{}
	args := []any{existing.ID, tenantID}
	changed := false
	if changes.DisplayNameSet && changes.DisplayName != existing.Name {
		args = append(args, changes.DisplayName)
		setParts = append(setParts, fmt.Sprintf("name = $%d", len(args)))
		changed = true
	}
	if changes.ExternalIDSet && !sameSCIMExternalID(changes.ExternalID, existing.ExternalID) {
		args = append(args, changes.ExternalID)
		setParts = append(setParts, fmt.Sprintf("scim_external_id = $%d", len(args)))
		changed = true
	}
	if len(setParts) > 0 {
		setParts = append(setParts, "updated_at = now()")
		if _, err := tx.Exec(ctx, `UPDATE groups SET `+strings.Join(setParts, ", ")+` WHERE id = $1 AND tenant_id = $2`, args...); err != nil {
			if isUniqueViolation(err) {
				return existing, false, nil, newSCIMHandlerError(http.StatusConflict, "uniqueness", "externalId is already in use")
			}
			s.logger.Warn("SCIM group update failed")
			return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
		}
	}
	var roleChanges []scimRoleChange
	if changes.MembersSet {
		currentIDs, err := scimGroupMemberIDsTx(ctx, tx, existing.ID)
		if err != nil {
			s.logger.Warn("SCIM group membership load failed")
			return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
		}
		added, removed := scimMembershipDiff(currentIDs, changes.MemberIDs)
		for _, id := range removed {
			if _, err := tx.Exec(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, existing.ID, id); err != nil {
				s.logger.Warn("SCIM group member removal failed")
				return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
			}
		}
		for _, id := range added {
			if _, err := tx.Exec(ctx, `
				INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
				ON CONFLICT (group_id, user_id) DO NOTHING
			`, existing.ID, id); err != nil {
				s.logger.Warn("SCIM group member add failed")
				return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
			}
		}
		if len(added) > 0 || len(removed) > 0 {
			changed = true
			// Membership is part of the resource; bump lastModified even when no
			// scalar attribute changed.
			if _, err := tx.Exec(ctx, `UPDATE groups SET updated_at = now() WHERE id = $1 AND tenant_id = $2`, existing.ID, tenantID); err != nil {
				s.logger.Warn("SCIM group membership timestamp update failed")
				return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
			}
		}
		affected := append(append([]string{}, currentIDs...), changes.MemberIDs...)
		roleChanges, err = s.scimRecomputeRolesTx(ctx, tx, tenantID, affected)
		if err != nil {
			s.logger.Warn("SCIM role recompute failed")
			return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
		}
	}
	updated, err := scanSCIMGroup(tx.QueryRow(ctx,
		`SELECT `+scimGroupColumns+` FROM groups WHERE id = $1 AND tenant_id = $2`, existing.ID, tenantID))
	if err != nil {
		s.logger.Warn("SCIM group reload failed")
		return existing, false, nil, newSCIMHandlerError(http.StatusInternalServerError, "", "Could not update the SCIM group")
	}
	return updated, changed, roleChanges, nil
}

// scimGroupMemberIDsTx returns the stored membership ids without filtering
// soft-deleted users, because removal must still be able to delete those rows.
func scimGroupMemberIDsTx(ctx context.Context, tx pgx.Tx, groupID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT user_id::text FROM group_members WHERE group_id = $1 ORDER BY user_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// registerSCIMGroupRoutes mounts the Groups collection and item endpoints
// behind bearer authentication, ahead of the /scim/v2/ fallback.
func (s *Server) registerSCIMGroupRoutes(mux *http.ServeMux) {
	mux.Handle("/scim/v2/Groups", s.scimAuth(http.HandlerFunc(s.scimGroupsCollection)))
	mux.Handle("/scim/v2/Groups/{id}", s.scimAuth(http.HandlerFunc(s.scimGroupsItem)))
}

func (s *Server) scimGroupsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.scimListGroups(w, r)
	case http.MethodPost:
		s.scimCreateGroup(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeSCIMError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
	}
}

func (s *Server) scimGroupsItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.scimGetGroup(w, r)
	case http.MethodPut:
		s.scimReplaceGroup(w, r)
	case http.MethodPatch:
		s.scimPatchGroup(w, r)
	case http.MethodDelete:
		s.scimDeleteGroup(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH, DELETE")
		writeSCIMError(w, http.StatusMethodNotAllowed, "", "Method not allowed")
	}
}

func (s *Server) scimListGroups(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	startIndex, count, err := parseSCIMPagination(r)
	if err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	where := "tenant_id = $1"
	args := []any{tenant.TenantID}
	if rawFilter := strings.TrimSpace(r.URL.Query().Get("filter")); rawFilter != "" {
		field, value, filterErr := parseSCIMFilter(rawFilter, map[string]bool{"displayName": true, "externalId": true})
		if filterErr != nil {
			writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "Only displayName and externalId equality filters are supported")
			return
		}
		switch field {
		case "displayName":
			args = append(args, strings.TrimSpace(value))
			where += fmt.Sprintf(" AND lower(name) = lower($%d)", len(args))
		case "externalId":
			args = append(args, strings.TrimSpace(value))
			where += fmt.Sprintf(" AND scim_external_id = $%d", len(args))
		default:
			writeSCIMError(w, http.StatusBadRequest, "invalidFilter", "Only displayName and externalId equality filters are supported")
			return
		}
	}
	ctx := r.Context()
	var total int
	if err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM groups WHERE `+where, args...).Scan(&total); err != nil {
		s.logger.Warn("SCIM group list count failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM groups")
		return
	}
	pageArgs := append(append([]any{}, args...), count, startIndex-1)
	rows, err := s.store.DB.Query(ctx, `SELECT `+scimGroupColumns+` FROM groups WHERE `+where+
		fmt.Sprintf(" ORDER BY name, id LIMIT $%d OFFSET $%d", len(pageArgs)-1, len(pageArgs)), pageArgs...)
	if err != nil {
		s.logger.Warn("SCIM group list failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM groups")
		return
	}
	defer rows.Close()
	groups := []scimGroupRow{}
	for rows.Next() {
		group, scanErr := scanSCIMGroup(rows)
		if scanErr != nil {
			s.logger.Warn("SCIM group list scan failed")
			writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM groups")
			return
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("SCIM group list iteration failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM groups")
		return
	}
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	members, err := scimGroupMembersByGroup(ctx, s.store.DB, tenant.TenantID, ids)
	if err != nil {
		s.logger.Warn("SCIM group member list failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not list the SCIM groups")
		return
	}
	resources := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		resources = append(resources, scimGroupResource(group, members[group.ID], base))
	}
	writeSCIMJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{scimListResponseSchema},
		"totalResults": total,
		"itemsPerPage": len(resources),
		"startIndex":   startIndex,
		"Resources":    resources,
	})
}

func (s *Server) scimGetGroup(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	group, err := s.scimGroupByID(r.Context(), tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM group not found")
			return
		}
		s.logger.Warn("SCIM group lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not load the SCIM group")
		return
	}
	members, err := scimGroupMembersFor(r.Context(), s.store.DB, tenant.TenantID, group.ID)
	if err != nil {
		s.logger.Warn("SCIM group member lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not load the SCIM group")
		return
	}
	writeSCIMJSON(w, http.StatusOK, scimGroupResource(group, members, base))
}

func (s *Server) scimCreateGroup(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	var payload scimGroupPayload
	if err := decodeSCIMRequest(w, r, &payload); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	name, handlerErr := normalizeSCIMGroupDisplayName(payload.DisplayName)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	if payload.ExternalID.Set {
		if handlerErr := validateSCIMExternalIDText(payload.ExternalID.Value); handlerErr != nil {
			writeSCIMHandlerError(w, handlerErr)
			return
		}
	}
	externalID := normalizeSCIMExternalID(payload.ExternalID)
	memberIDs := []string{}
	for _, member := range payload.Members {
		value := strings.TrimSpace(member.Value)
		if value == "" {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "members value is required")
			return
		}
		memberIDs = append(memberIDs, value)
	}
	memberIDs = scimUniqueStrings(memberIDs)

	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	defer tx.Rollback(ctx)

	if handlerErr := scimValidateGroupMemberIDs(ctx, tx, tenant.TenantID, memberIDs); handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	if externalID != nil {
		var conflict bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM groups WHERE tenant_id = $1 AND scim_external_id = $2)
		`, tenant.TenantID, *externalID).Scan(&conflict); err != nil {
			s.logger.Warn("SCIM group external id lookup failed")
			writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
			return
		}
		if conflict {
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A group with this externalId already exists")
			return
		}
	}
	domain, err := scimGroupDomainTx(ctx, tx, tenant.TenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusBadRequest, "invalidValue", "A verified organization domain is required before creating SCIM groups")
			return
		}
		s.logger.Warn("SCIM group domain lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	var created scimGroupRow
	created.Name = name
	created.Email = scimGroupInternalEmail(domain)
	created.ExternalID = externalID
	created.GroupType = "security"
	created.Status = "active"
	err = tx.QueryRow(ctx, `
		INSERT INTO groups (tenant_id, name, email, description, group_type, status, scim_external_id)
		VALUES ($1, $2, $3, '', 'security', 'active', $4)
		RETURNING id::text, created_at, updated_at
	`, tenant.TenantID, name, created.Email, externalID).Scan(&created.ID, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeSCIMError(w, http.StatusConflict, "uniqueness", "A group with this externalId already exists")
			return
		}
		s.logger.Warn("SCIM group insert failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	for _, memberID := range memberIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
			ON CONFLICT (group_id, user_id) DO NOTHING
		`, created.ID, memberID); err != nil {
			s.logger.Warn("SCIM group member insert failed")
			writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
			return
		}
	}
	roleChanges, err := s.scimRecomputeRolesTx(ctx, tx, tenant.TenantID, memberIDs)
	if err != nil {
		s.logger.Warn("SCIM role recompute failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	members, err := scimGroupMembersFor(ctx, tx, tenant.TenantID, created.ID)
	if err != nil {
		s.logger.Warn("SCIM group member reload failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not create the SCIM group")
		return
	}
	s.scimAudit(r, tenant, "scim.group_created", created.Email, "Created group through SCIM")
	s.scimAuditRoleChanges(r, tenant, roleChanges)
	w.Header().Set("Location", base+"/Groups/"+created.ID)
	writeSCIMJSON(w, http.StatusCreated, scimGroupResource(created, members, base))
}

func (s *Server) scimReplaceGroup(w http.ResponseWriter, r *http.Request) {
	tenant, base, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	var payload scimGroupPayload
	if err := decodeSCIMRequest(w, r, &payload); err != nil {
		writeSCIMError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	changes, handlerErr := scimGroupReplaceChanges(payload)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	s.scimWriteGroup(w, r, tenant, base, changes)
}

func (s *Server) scimPatchGroup(w http.ResponseWriter, r *http.Request) {
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
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimGroupByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM group not found")
			return
		}
		s.logger.Warn("SCIM group lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	currentIDs, err := scimGroupMemberIDsTx(ctx, tx, existing.ID)
	if err != nil {
		s.logger.Warn("SCIM group membership load failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	changes, handlerErr := scimGroupPatchChanges(payload, currentIDs)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	updated, changed, roleChanges, handlerErr := s.scimApplyGroupChanges(ctx, tx, tenant.TenantID, existing, changes)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	members, err := scimGroupMembersFor(ctx, tx, tenant.TenantID, updated.ID)
	if err != nil {
		s.logger.Warn("SCIM group member reload failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	if changed {
		s.scimAudit(r, tenant, "scim.group_updated", updated.Email, "Updated group through SCIM")
	}
	s.scimAuditRoleChanges(r, tenant, roleChanges)
	writeSCIMJSON(w, http.StatusOK, scimGroupResource(updated, members, base))
}

// scimWriteGroup performs the shared PUT transaction: load, apply and commit.
func (s *Server) scimWriteGroup(w http.ResponseWriter, r *http.Request, tenant scimTenant, base string, changes scimGroupChangeSet) {
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimGroupByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM group not found")
			return
		}
		s.logger.Warn("SCIM group lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	updated, changed, roleChanges, handlerErr := s.scimApplyGroupChanges(ctx, tx, tenant.TenantID, existing, changes)
	if handlerErr != nil {
		writeSCIMHandlerError(w, handlerErr)
		return
	}
	members, err := scimGroupMembersFor(ctx, tx, tenant.TenantID, updated.ID)
	if err != nil {
		s.logger.Warn("SCIM group member reload failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not update the SCIM group")
		return
	}
	if changed {
		s.scimAudit(r, tenant, "scim.group_updated", updated.Email, "Updated group through SCIM")
	}
	s.scimAuditRoleChanges(r, tenant, roleChanges)
	writeSCIMJSON(w, http.StatusOK, scimGroupResource(updated, members, base))
}

func (s *Server) scimDeleteGroup(w http.ResponseWriter, r *http.Request) {
	tenant, _, ok := scimRequestIdentity(s, w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	defer tx.Rollback(ctx)
	existing, err := scimGroupByIDTx(ctx, tx, tenant.TenantID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeSCIMError(w, http.StatusNotFound, "", "SCIM group not found")
			return
		}
		s.logger.Warn("SCIM group lookup failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	formerMembers, err := scimGroupMemberIDsTx(ctx, tx, existing.ID)
	if err != nil {
		s.logger.Warn("SCIM group membership load failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM groups WHERE id = $1 AND tenant_id = $2`, existing.ID, tenant.TenantID); err != nil {
		s.logger.Warn("SCIM group delete failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	roleChanges, err := s.scimRecomputeRolesTx(ctx, tx, tenant.TenantID, formerMembers)
	if err != nil {
		s.logger.Warn("SCIM role recompute failed")
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeSCIMError(w, http.StatusInternalServerError, "", "Could not delete the SCIM group")
		return
	}
	s.scimAudit(r, tenant, "scim.group_deleted", existing.Email, "Deleted group through SCIM")
	s.scimAuditRoleChanges(r, tenant, roleChanges)
	w.WriteHeader(http.StatusNoContent)
}
