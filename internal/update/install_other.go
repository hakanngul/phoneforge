//go:build !darwin

package update

// BundleInfo helpers and the installer are macOS-only; everything else is notify-only.

type unsupportedInstaller struct{ opts InstallerOptions }

// NewPlatformInstaller returns an installer that supports nothing (notify-only).
func NewPlatformInstaller(opts InstallerOptions) Installer { return unsupportedInstaller{opts} }

func (unsupportedInstaller) Supported() (bool, string) {
	return false, "automatic installation is not supported on this platform; download the new version manually"
}
func (unsupportedInstaller) Prepare(string, *Manifest) (string, error) { return "", ErrUnsupported }
func (unsupportedInstaller) Apply(string, string) (string, error)      { return "", ErrUnsupported }
func (unsupportedInstaller) Relaunch() error                           { return ErrUnsupported }
func (unsupportedInstaller) Rollback(string) error                     { return ErrUnsupported }

// RunningApp is unsupported off macOS.
func RunningApp() (string, error) { return "", ErrUnsupported }

// InspectBundle is unsupported off macOS.
func InspectBundle(string) (BundleInfo, error) { return BundleInfo{}, ErrUnsupported }

// VerifyCandidate is unsupported off macOS.
func VerifyCandidate(cur, cand BundleInfo, expectVersion string, allowAdhoc bool) error {
	return ErrUnsupported
}

// Extract is unsupported off macOS.
func Extract(zipPath, destDir string) (string, error) { return "", ErrUnsupported }

// Preflight is unsupported off macOS.
func Preflight(currentApp string) error { return ErrUnsupported }

// Swap is unsupported off macOS.
func Swap(candidate, currentApp, backupDir string) (string, error) { return "", ErrUnsupported }

// Rollback is unsupported off macOS.
func Rollback(currentApp, backupApp string) error { return ErrUnsupported }

// Relaunch is unsupported off macOS.
func Relaunch(appPath string, waitPID int) error { return ErrUnsupported }
