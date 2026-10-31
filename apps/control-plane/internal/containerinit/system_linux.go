//go:build linux && (amd64 || arm64)

package containerinit

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	// The values below are identical on linux/amd64 and linux/arm64, the
	// only architectures the production image supports.
	openPathOnly         = 0x200000
	atEmptyPath          = 0x1000
	prSetNoNewPrivileges = 38
)

type linuxSystem struct{}

func newSystem() (system, error) {
	return linuxSystem{}, nil
}

func (linuxSystem) identity() (identity, error) {
	var current identity
	var realUID, effectiveUID, savedUID uint32
	if _, _, errno := syscall.RawSyscall(syscall.SYS_GETRESUID, uintptr(unsafe.Pointer(&realUID)), uintptr(unsafe.Pointer(&effectiveUID)), uintptr(unsafe.Pointer(&savedUID))); errno != 0 {
		return identity{}, errno
	}
	var realGID, effectiveGID, savedGID uint32
	if _, _, errno := syscall.RawSyscall(syscall.SYS_GETRESGID, uintptr(unsafe.Pointer(&realGID)), uintptr(unsafe.Pointer(&effectiveGID)), uintptr(unsafe.Pointer(&savedGID))); errno != 0 {
		return identity{}, errno
	}
	groups, err := syscall.Getgroups()
	if err != nil {
		return identity{}, err
	}
	current.realUID, current.effectiveUID, current.savedUID = int(realUID), int(effectiveUID), int(savedUID)
	current.realGID, current.effectiveGID, current.savedGID = int(realGID), int(effectiveGID), int(savedGID)
	current.groups = groups
	return current, nil
}

func (linuxSystem) openDirectory(path string) (int, error) {
	return syscall.Open(path, openPathOnly|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

func (linuxSystem) openAt(directory int, name string) (int, error) {
	return syscall.Openat(directory, name, openPathOnly|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

func (linuxSystem) fstat(descriptor int) (fileInfo, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(descriptor, &stat); err != nil {
		return fileInfo{}, err
	}
	return linuxFileInfo(&stat), nil
}

func (linuxSystem) fchown(descriptor, uid, gid int) error {
	return syscall.Fchownat(descriptor, "", uid, gid, atEmptyPath)
}

func (linuxSystem) close(descriptor int) error {
	return syscall.Close(descriptor)
}

func (linuxSystem) lstat(path string) (fileInfo, error) {
	var stat syscall.Stat_t
	if err := syscall.Lstat(path, &stat); err != nil {
		return fileInfo{}, err
	}
	return linuxFileInfo(&stat), nil
}

func (linuxSystem) setgroups(groups []int) error {
	return syscall.Setgroups(groups)
}

func (linuxSystem) setgid(gid int) error {
	return syscall.Setgid(gid)
}

func (linuxSystem) setuid(uid int) error {
	return syscall.Setuid(uid)
}

// setNoNewPrivileges sets the flag on the calling thread and keeps the
// goroutine locked to it, so the following exec runs on the same thread.
func (linuxSystem) setNoNewPrivileges() error {
	runtime.LockOSThread()
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivileges, 1, 0); errno != 0 {
		return errno
	}
	return nil
}

func (linuxSystem) exec(path string, argv, environment []string) error {
	return syscall.Exec(path, argv, environment)
}

func linuxFileInfo(stat *syscall.Stat_t) fileInfo {
	info := fileInfo{uid: int(stat.Uid), gid: int(stat.Gid), nlink: uint64(stat.Nlink)}
	switch stat.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		info.kind = kindDirectory
	case syscall.S_IFREG:
		info.kind = kindRegular
	case syscall.S_IFSOCK:
		info.kind = kindSocket
	case syscall.S_IFLNK:
		info.kind = kindSymlink
	default:
		info.kind = kindOther
	}
	return info
}
