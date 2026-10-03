package guest

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Credential is who the command runs as.
type Credential struct {
	UID    uint32
	GID    uint32
	Groups []uint32
	Home   string
}

type passwdEntry struct {
	name     string
	uid, gid uint32
	home     string
}

type groupEntry struct {
	name    string
	gid     uint32
	members []string
}

// LookupUser resolves spec against the passwd and group files of the
// filesystem at root. spec takes the forms Docker accepts for an
// image's User: "", "user", "uid", "user:group", "uid:gid" and mixes of
// those. Numeric IDs don't need to exist in the files; names do.
func LookupUser(root, spec string) (Credential, error) {
	users, err := readPasswd(filepath.Join(root, "etc", "passwd"))
	if err != nil {
		return Credential{}, err
	}
	groups, err := readGroup(filepath.Join(root, "etc", "group"))
	if err != nil {
		return Credential{}, err
	}

	userPart, groupPart, hasGroup := strings.Cut(spec, ":")
	if userPart == "" {
		userPart = "0"
	}

	cred := Credential{Home: "/"}
	var name string

	uid, numeric := parseID(userPart)
	idx := slices.IndexFunc(users, func(u passwdEntry) bool {
		if numeric {
			return u.uid == uid
		}
		return u.name == userPart
	})
	switch {
	case idx >= 0:
		u := users[idx]
		name = u.name
		cred.UID, cred.GID, cred.Home = u.uid, u.gid, u.home
	case numeric:
		cred.UID = uid
	default:
		return Credential{}, fmt.Errorf("user %q not found in /etc/passwd", userPart)
	}

	if hasGroup && groupPart != "" {
		if gid, ok := parseID(groupPart); ok {
			cred.GID = gid
		} else {
			idx := slices.IndexFunc(groups, func(g groupEntry) bool { return g.name == groupPart })
			if idx < 0 {
				return Credential{}, fmt.Errorf("group %q not found in /etc/group", groupPart)
			}
			cred.GID = groups[idx].gid
		}
	}

	if name != "" {
		for _, g := range groups {
			if g.gid != cred.GID && slices.Contains(g.members, name) && !slices.Contains(cred.Groups, g.gid) {
				cred.Groups = append(cred.Groups, g.gid)
			}
		}
	}

	return cred, nil
}

func parseID(s string) (uint32, bool) {
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err == nil
}

// readColonFile returns the fields of each line of a passwd-style file.
// A missing file is treated as empty: plenty of images have no
// /etc/passwd.
func readColonFile(path string) ([][]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines [][]string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, strings.Split(line, ":"))
	}
	return lines, s.Err()
}

func readPasswd(path string) ([]passwdEntry, error) {
	lines, err := readColonFile(path)
	if err != nil {
		return nil, err
	}

	var entries []passwdEntry
	for _, f := range lines {
		if len(f) < 6 {
			continue
		}
		uid, ok1 := parseID(f[2])
		gid, ok2 := parseID(f[3])
		if !ok1 || !ok2 {
			continue
		}
		entries = append(entries, passwdEntry{name: f[0], uid: uid, gid: gid, home: f[5]})
	}
	return entries, nil
}

func readGroup(path string) ([]groupEntry, error) {
	lines, err := readColonFile(path)
	if err != nil {
		return nil, err
	}

	var entries []groupEntry
	for _, f := range lines {
		if len(f) < 3 {
			continue
		}
		gid, ok := parseID(f[2])
		if !ok {
			continue
		}
		g := groupEntry{name: f[0], gid: gid}
		if len(f) > 3 && f[3] != "" {
			g.members = strings.Split(f[3], ",")
		}
		entries = append(entries, g)
	}
	return entries, nil
}
