package control

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type lsblkNode struct {
	Name        string      `json:"name"`
	Path        string      `json:"path"`
	Type        string      `json:"type"`
	Size        any         `json:"size"`
	FSType      *string     `json:"fstype"`
	Label       *string     `json:"label"`
	UUID        *string     `json:"uuid"`
	Mountpoint  *string     `json:"mountpoint"`
	Mountpoints []string    `json:"mountpoints"`
	ROTA        any         `json:"rota"`
	Model       *string     `json:"model"`
	Serial      *string     `json:"serial"`
	WWN         *string     `json:"wwn"`
	TRAN        *string     `json:"tran"`
	PKName      *string     `json:"pkname"`
	Children    []lsblkNode `json:"children"`
}

type storageDisk struct {
	Name            string             `json:"name"`
	Path            string             `json:"path"`
	Type            string             `json:"type"`
	SizeBytes       uint64             `json:"sizeBytes"`
	FSType          string             `json:"fsType"`
	Label           string             `json:"label"`
	UUID            string             `json:"uuid"`
	Mountpoint      string             `json:"mountpoint"`
	Rotational      bool               `json:"rotational"`
	Model           string             `json:"model"`
	Serial          string             `json:"serial"`
	WWN             string             `json:"wwn"`
	Transport       string             `json:"transport"`
	Parent          string             `json:"parent"`
	Candidate       bool               `json:"candidate"`
	Protected       bool               `json:"protected"`
	SystemProtected bool               `json:"systemProtected"`
	Protection      string             `json:"protection,omitempty"`
	HasSignatures   bool               `json:"hasSignatures"`
	Topology        string             `json:"topology"`
	TopologyReason  string             `json:"topologyReason,omitempty"`
	Health          *storageDiskHealth `json:"health,omitempty"`
	Children        []storageDisk      `json:"children,omitempty"`
}

var storageDiscoveryCommand = runCommand
var storageHostGOOS = runtime.GOOS

func (s *Server) listStorageDisks(ctx context.Context) ([]storageDisk, bool, string) {
	if storageHostGOOS != "linux" {
		return []storageDisk{}, false, "Block-device discovery is available on the Linux host only."
	}
	result, err := storageDiscoveryCommand(ctx, 10*time.Second, "lsblk", "-J", "-b", "-o",
		"NAME,PATH,TYPE,SIZE,FSTYPE,LABEL,UUID,MOUNTPOINT,ROTA,MODEL,SERIAL,WWN,TRAN,PKNAME")
	if err != nil && strings.TrimSpace(result.Stdout) == "" {
		return []storageDisk{}, false, "lsblk is unavailable on this host: " + err.Error()
	}
	var payload struct {
		Blockdevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return []storageDisk{}, false, "Could not parse lsblk output."
	}
	devices := make([]storageDisk, 0, len(payload.Blockdevices))
	for _, node := range payload.Blockdevices {
		devices = append(devices, normalizeStorageDisk(node, ""))
	}
	return devices, true, ""
}

func (s *Server) handleStorageDisks(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	devices, supported, message := s.listStorageDisks(r.Context())
	if !supported {
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": false, "message": message, "devices": []storageDisk{},
		})
		return
	}
	for index := range devices {
		if devices[index].Type == "disk" {
			devices[index].Health = s.storageDiskHealth(r.Context(), devices[index])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"supported": true, "generatedAt": nowRFC3339(), "devices": devices,
	})
}

func normalizeStorageDisk(node lsblkNode, parent string) storageDisk {
	device := storageDisk{
		Name:       strings.TrimSpace(node.Name),
		Path:       strings.TrimSpace(node.Path),
		Type:       strings.TrimSpace(node.Type),
		SizeBytes:  storageDiskSize(node.Size),
		FSType:     storageDiskString(node.FSType),
		Label:      storageDiskString(node.Label),
		UUID:       storageDiskString(node.UUID),
		Mountpoint: storageDiskMountpoint(node),
		Rotational: storageDiskBool(node.ROTA),
		Model:      storageDiskString(node.Model),
		Serial:     storageDiskString(node.Serial),
		WWN:        storageDiskString(node.WWN),
		Transport:  storageDiskString(node.TRAN),
		Parent:     parent,
	}
	device.HasSignatures = device.FSType != "" || device.UUID != "" || device.Label != ""
	device.Topology = storageBlockTopology(node.Type)
	switch device.Topology {
	case "luks":
		device.SystemProtected = true
		device.Protection = "LUKS-encrypted device"
		device.TopologyReason = "LUKS encryption must be managed on the host; Alt8Mail does not unlock or format encrypted devices."
	case "raid":
		device.SystemProtected = true
		device.Protection = "RAID device or member"
		device.TopologyReason = "RAID creation and member changes are deferred until a separate RAID design and lab matrix exist."
	case "multipath":
		device.SystemProtected = true
		device.Protection = "Multipath device"
		device.TopologyReason = "Multipath/SAN fabric configuration is not automated."
	}
	if node.Type == "rom" || node.Type == "loop" || node.Type == "zram" {
		device.Protected = true
		device.SystemProtected = true
		device.Protection = "Virtual or removable device"
	}
	if device.Mountpoint != "" {
		switch device.Mountpoint {
		case "/", "/boot", "/boot/efi", "/efi", "[SWAP]":
			device.Protected = true
			device.SystemProtected = true
			device.Protection = "Mounted system filesystem"
		}
	}
	if node.Type == "disk" && len(node.Children) > 0 {
		device.Protected = true
		if device.Protection == "" {
			device.Protection = "Has partitions or child devices"
		}
	}
	device.Candidate = node.Type == "disk" && !device.Protected && device.Mountpoint == "" && device.SizeBytes >= 1<<30
	for _, child := range node.Children {
		device.Children = append(device.Children, normalizeStorageDisk(child, device.Name))
	}
	for _, child := range device.Children {
		switch child.Topology {
		case "luks":
			device.SystemProtected = true
			device.Protection = "Contains a LUKS-encrypted volume"
			device.TopologyReason = "LUKS encryption must be managed on the host; Alt8Mail does not unlock or format encrypted devices."
		case "raid":
			device.SystemProtected = true
			device.Protection = "Contains a RAID member or array"
			device.TopologyReason = "RAID creation and member changes are deferred until a separate RAID design and lab matrix exist."
		case "multipath":
			device.SystemProtected = true
			device.Protection = "Contains a multipath device"
			device.TopologyReason = "Multipath/SAN fabric configuration is not automated."
		case "lvm":
			if device.Topology == "local" {
				device.Topology = "lvm"
				device.TopologyReason = "The disk contains an LVM physical volume; formatting requires storage.destroy."
			}
		}
	}
	return device
}

func storageBlockTopology(blockType string) string {
	switch {
	case blockType == "crypt":
		return "luks"
	case strings.HasPrefix(blockType, "raid"):
		return "raid"
	case blockType == "mpath":
		return "multipath"
	case blockType == "lvm":
		return "lvm"
	case blockType == "disk" || blockType == "part":
		return "local"
	default:
		return blockType
	}
}

func storageDiskString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func storageDiskMountpoint(node lsblkNode) string {
	if node.Mountpoint != nil && strings.TrimSpace(*node.Mountpoint) != "" {
		return strings.TrimSpace(*node.Mountpoint)
	}
	for _, mountpoint := range node.Mountpoints {
		if strings.TrimSpace(mountpoint) != "" {
			return strings.TrimSpace(mountpoint)
		}
	}
	return ""
}

func storageDiskSize(value any) uint64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := strconv.ParseUint(typed.String(), 10, 64)
		return parsed
	case float64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case string:
		parsed, _ := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		return parsed
	}
	return 0
}

func storageDiskBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	case float64:
		return typed != 0
	}
	return false
}
