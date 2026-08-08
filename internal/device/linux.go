package device

import (
	"context"
	"errors"
)

// LinuxProvider will discover SD/CFexpress readers as Linux block devices,
// preferably via a filesystem UGOS/udev already mounted read-only
// (Strategy A), falling back to /dev + manual mount (Strategy B) if that
// isn't available. See RUNBOOK_MOY_MEDIA_INGEST.md section 9.
//
// This requires validating hot-plug, stable device identifiers
// (/dev/disk/by-path, not /dev/sdX), and mount/umount behavior first under
// WSL2 + usbipd-win and then physically on the UGOS Pro NAS. Deliberately
// not implemented for the mock-mode MVP.
type LinuxProvider struct{}

func NewLinuxProvider() *LinuxProvider {
	return &LinuxProvider{}
}

func (p *LinuxProvider) Name() string { return "linux" }

func (p *LinuxProvider) Scan(ctx context.Context) ([]Source, error) {
	return nil, errors.New("INGEST_MODE=real is not implemented yet: needs USB/block-device validation on WSL2 (usbipd-win) and UGOS Pro before use")
}
