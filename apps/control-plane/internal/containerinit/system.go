package containerinit

type fileKind int

const (
	kindOther fileKind = iota
	kindDirectory
	kindRegular
	kindSocket
	kindSymlink
)

type fileInfo struct {
	kind  fileKind
	uid   int
	gid   int
	nlink uint64
}

type identity struct {
	realUID, effectiveUID, savedUID int
	realGID, effectiveGID, savedGID int
	groups                          []int
}

func (i identity) includesRoot() bool {
	for _, value := range []int{i.realUID, i.effectiveUID, i.savedUID, i.realGID, i.effectiveGID, i.savedGID} {
		if value == 0 {
			return true
		}
	}
	for _, group := range i.groups {
		if group == 0 {
			return true
		}
	}
	return false
}

// system is the narrow operating-system boundary used by start. Descriptor
// operations work on path-only handles opened without following the final
// symlink, so ownership changes apply to the inspected inode and never to a
// path that was replaced after inspection.
type system interface {
	identity() (identity, error)
	openDirectory(path string) (int, error)
	openAt(directory int, name string) (int, error)
	fstat(descriptor int) (fileInfo, error)
	fchown(descriptor, uid, gid int) error
	close(descriptor int) error
	lstat(path string) (fileInfo, error)
	setgroups(groups []int) error
	setgid(gid int) error
	setuid(uid int) error
	setNoNewPrivileges() error
	exec(path string, argv, environment []string) error
}
