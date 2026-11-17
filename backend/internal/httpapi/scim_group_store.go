package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This file keeps the group store and role-mapping logic that the core admin
// console uses for local groups. The SCIM HTTP surface and its resource
// mapping live in the private enterprise module and call back through the
// SCIMAccess host interface for role recomputation.

// scimGroupColumns is the projection shared by every Groups query.
const scimGroupColumns = `id::text, name, email::text, description, scim_external_id, scim_role,
	group_type, status, created_at, updated_at`

// scimGroupRow is an internal group row.
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

// scimUniqueStrings preserves first-seen order and drops duplicates.
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

func scanSCIMGroup(row pgx.Row) (scimGroupRow, error) {
	var group scimGroupRow
	err := row.Scan(&group.ID, &group.Name, &group.Email, &group.Description, &group.ExternalID, &group.ScimRole,
		&group.GroupType, &group.Status, &group.CreatedAt, &group.UpdatedAt)
	return group, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// scimGroupByIDTx loads one tenant-scoped group under a row lock.
func scimGroupByIDTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (scimGroupRow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return scimGroupRow{}, pgx.ErrNoRows
	}
	return scanSCIMGroup(tx.QueryRow(ctx,
		`SELECT `+scimGroupColumns+` FROM groups WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
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

// scimRecomputeUserRole applies the local group→role mapping to one user inside
// the caller's transaction.
//
//   - Owners are never touched, so SCIM can neither grant nor demote owner.
//   - Platform system administrators are never touched: their lifecycle and
//     role are managed only in the admin console.
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
	var isSystemAdmin bool
	err := tx.QueryRow(ctx, `
		SELECT email::text, role, role_source, deleted_at, is_system_admin
		FROM users WHERE id = $1 AND tenant_id = $2
		FOR UPDATE
	`, userID, tenantID).Scan(&email, &role, &roleSource, &deletedAt, &isSystemAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return scimRoleChange{}, nil
	}
	if err != nil {
		return scimRoleChange{}, err
	}
	change := scimRoleChange{UserID: userID, Email: email, OldRole: role}
	if deletedAt != nil || role == "owner" || isSystemAdmin {
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

// adminGroupRoleAuditAction maps one recompute result to the admin-origin audit
// action so SIEM filters can distinguish IdP-driven changes from console ones.
func adminGroupRoleAuditAction(change scimRoleChange) (action, detail string) {
	mapped, detail := scimRoleAuditDetail(change)
	if mapped {
		return "admin.role_mapped", detail
	}
	return "admin.role_unmapped", detail
}

// adminAuditRoleChanges records mapped/unmapped transitions for admin-origin
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
