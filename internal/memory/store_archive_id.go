package memory

import "path/filepath"

// archiveByID archives every active copy of a fact that shares the given ID
// across directories (migration duplicates share the deterministic ID), while
// same-named facts with a different ID stay put. Returns the last path
// archived. The unqualified-name path already archives from every directory.
func (s Store) archiveByID(name, id string) (string, error) {
	var lastPath string
	for _, dir := range s.dirs() {
		if dir == "" || !memoryIDInDir(dir, name, id) {
			continue
		}
		p, err := archiveMemoryInDir(dir, name)
		if err != nil {
			return "", err
		}
		if p != "" {
			lastPath = p
		}
	}
	return lastPath, nil
}

// memoryIDInDir reports whether dir holds an active fact with the given ID.
// Used to archive every migration duplicate of an ID while leaving same-named
// facts with a different identity untouched.
func memoryIDInDir(dir, name, id string) bool {
	m, ok := loadMemory(filepath.Join(dir, name+".md"))
	return ok && m.ID == id
}
