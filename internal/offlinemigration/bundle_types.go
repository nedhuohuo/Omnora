package offlinemigration

import (
	"time"

	"omnora/internal/store"
)

// BundleRequest identifies every persistent root that must be captured before
// an offline migration. External mounts are deliberately represented only by
// durable identity metadata; their content is never copied into the bundle.
type BundleRequest struct {
	DB                 *store.DB
	DBPath             string
	ConfigDir          string
	DataDir            string
	ManagedDir         string
	RollbackRoot       string
	InstanceID         string
	SourceMigration    string
	TargetMigration    string
	ExternalIdentities []ExternalIdentity

	testHooks *bundleTestHooks
}

type ExternalIdentity struct {
	RegistrationID string `json:"registration_id"`
	Root           string `json:"root"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	MountID        uint64 `json:"mount_id,omitempty"`
	Filesystem     string `json:"filesystem,omitempty"`
	Source         string `json:"source,omitempty"`
}

type BundleResult struct {
	ID            string
	Path          string
	ManifestPath  string
	SnapshotPath  string
	RequiredBytes uint64
}

type BundleManifest struct {
	FormatVersion      int                `json:"format_version"`
	BundleID           string             `json:"bundle_id"`
	CreatedAt          time.Time          `json:"created_at"`
	InstanceID         string             `json:"instance_id"`
	SourceMigration    string             `json:"source_migration"`
	TargetMigration    string             `json:"target_migration"`
	Files              []ManifestEntry    `json:"files"`
	ExternalIdentities []ExternalIdentity `json:"external_identities"`
	FileCount          int                `json:"file_count"`
	TotalBytes         uint64             `json:"total_bytes"`
}

type ManifestEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
}
