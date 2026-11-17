package control

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExtensionPrincipal is the authenticated control-panel actor passed to private
// paid modules.
type ExtensionPrincipal struct {
	UserID string
	Name   string
	Email  string
}

// ExtensionStorageVolume is the storage volume projection paid restore modules
// need to locate managed mail roots.
type ExtensionStorageVolume struct {
	Name      string
	MountPath string
	Role      string
}

// CommandResult is the exported result of a control-plane command.
type CommandResult = commandResult

// ExtensionHost is implemented by the control server for private paid modules.
type ExtensionHost interface {
	DB() *pgxpool.Pool
	Config() Config
	Principal(r *http.Request) (ExtensionPrincipal, bool)
	Audit(r *http.Request, principal ExtensionPrincipal, action, target, detail string, metadata map[string]any)
	AuditBackground(principal ExtensionPrincipal, action, target, detail string, metadata map[string]any)
	HandleFeature(pattern, permission, feature string, handler func(http.ResponseWriter, *http.Request, ExtensionPrincipal))
	RunCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (CommandResult, error)
	StorageVolumes(ctx context.Context) ([]ExtensionStorageVolume, error)
}

// Extension is implemented by the private enterprise module.
type Extension interface {
	RegisterControl(ExtensionHost) error
}

var controlExtension Extension

// RegisterControlExtension installs the private control extension.
func RegisterControlExtension(extension Extension) {
	controlExtension = extension
}

// RegisteredControlExtension returns the private extension, or nil in a
// community build.
func RegisteredControlExtension() Extension {
	return controlExtension
}

type controlExtensionHost struct {
	server *Server
	mux    *http.ServeMux
}

func (h controlExtensionHost) DB() *pgxpool.Pool { return h.server.db }
func (h controlExtensionHost) Config() Config    { return h.server.cfg }
func (h controlExtensionHost) Principal(r *http.Request) (ExtensionPrincipal, bool) {
	sess, err := h.server.authenticateSession(r)
	if err != nil || sess == nil {
		return ExtensionPrincipal{}, false
	}
	return ExtensionPrincipal{UserID: sess.User.ID, Name: sess.User.Name, Email: sess.User.Email}, true
}

func (h controlExtensionHost) Audit(r *http.Request, principal ExtensionPrincipal, action, target, detail string, metadata map[string]any) {
	h.server.audit(r.Context(), &adminUser{ID: principal.UserID, Name: principal.Name, Email: principal.Email},
		action, target, detail, h.server.requestIP(r), userAgent(r), metadata)
}

func (h controlExtensionHost) AuditBackground(principal ExtensionPrincipal, action, target, detail string, metadata map[string]any) {
	h.server.audit(context.Background(), &adminUser{ID: principal.UserID, Name: principal.Name, Email: principal.Email},
		action, target, detail, nil, "", metadata)
}

func (h controlExtensionHost) HandleFeature(pattern, permission, feature string, handler func(http.ResponseWriter, *http.Request, ExtensionPrincipal)) {
	h.mux.HandleFunc(pattern, h.server.require(permission, h.server.requireFeature(feature,
		func(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
			handler(w, r, ExtensionPrincipal{UserID: sess.User.ID, Name: sess.User.Name, Email: sess.User.Email})
		})))
}

func (h controlExtensionHost) RunCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (CommandResult, error) {
	return runCommand(ctx, timeout, name, args...)
}

func (h controlExtensionHost) StorageVolumes(ctx context.Context) ([]ExtensionStorageVolume, error) {
	volumes, err := h.server.storageVolumeRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ExtensionStorageVolume, 0, len(volumes))
	for _, volume := range volumes {
		out = append(out, ExtensionStorageVolume{Name: volume.Name, MountPath: volume.MountPath, Role: volume.Role})
	}
	return out, nil
}
