package control

import (
	"net"
	"time"
)

type adminUser struct {
	ID              string        `json:"id"`
	Email           string        `json:"email"`
	Name            string        `json:"name"`
	Role            string        `json:"role"`
	Status          string        `json:"status"`
	PasswordHash    string        `json:"-"`
	TOTPEnabled     bool          `json:"totpEnabled"`
	SessionTTL      int           `json:"sessionTtlSeconds"`
	FailedAttempts  int           `json:"-"`
	FailedWindow    *time.Time    `json:"-"`
	LockedUntil     *time.Time    `json:"lockedUntil"`
	LastLoginAt     *time.Time    `json:"lastLoginAt"`
	CreatedAt       time.Time     `json:"createdAt"`
	Permissions     []string      `json:"permissions,omitempty"`
	PermSet         permissionSet `json:"-"`
	Groups          []string      `json:"groups,omitempty"`
	TOTPSecret      string        `json:"-"`
	TOTPPending     string        `json:"-"`
	PasswordChanged bool          `json:"-"`
}

type sessionRecord struct {
	ID         string
	User       adminUser
	CSRF       string
	IP         net.IP
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type permissionSet map[string]bool

func (p permissionSet) Has(permission string) bool {
	if p == nil {
		return false
	}
	if p["*"] || p[permission] {
		return true
	}
	// A broad manage permission implies its read permission.
	if permission == "*.read" {
		return p["*"]
	}
	return false
}
