package update

// BundleInfo describes a macOS .app bundle.
type BundleInfo struct {
	Path      string
	BundleID  string
	Version   string
	TeamID    string // "" when ad-hoc signed
	Notarized bool   // spctl --assess --type execute passes
}

// InstallerOptions configures the platform installer.
type InstallerOptions struct {
	CurrentApp string // the running bundle; default RunningApp()
	StagingDir string // where archives are extracted (ideally same volume as CurrentApp)
	BackupDir  string // where the previous app is kept
	AllowAdhoc bool   // accept ad-hoc signed candidates (tests only)
}
