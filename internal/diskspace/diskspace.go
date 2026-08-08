// Package diskspace reports free/total space for a filesystem path. The
// app only ever runs on Linux (WSL2 or the UGOS Pro container), so this
// uses the stdlib syscall.Statfs directly instead of pulling in a
// cross-platform dependency for a one-syscall need.
package diskspace

import "syscall"

type Usage struct {
	BytesTotal uint64
	BytesFree  uint64
}

func (u Usage) PercentUsed() float64 {
	if u.BytesTotal == 0 {
		return 0
	}
	used := u.BytesTotal - u.BytesFree
	return float64(used) / float64(u.BytesTotal) * 100
}

func Check(path string) (Usage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Usage{}, err
	}
	blockSize := uint64(stat.Bsize)
	return Usage{
		BytesTotal: stat.Blocks * blockSize,
		BytesFree:  stat.Bavail * blockSize,
	}, nil
}
