package migrationcatalog

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

var filenamePattern = regexp.MustCompile(`^([0-9]{6})_[a-z0-9_]+\.sql$`)

type Entry struct {
	Version int64
	Name    string
}

type Catalog struct {
	Entries []Entry
}

func Inspect(fsys fs.FS) (Catalog, error) {
	if fsys == nil {
		return Catalog{}, errors.New("migration catalog is unavailable")
	}

	dirEntries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return Catalog{}, errors.New("migration catalog is unavailable")
	}

	catalog := Catalog{Entries: make([]Entry, 0, len(dirEntries))}
	versions := make(map[int64]string, len(dirEntries))
	for _, dirEntry := range dirEntries {
		name := dirEntry.Name()
		if !dirEntry.Type().IsRegular() {
			return Catalog{}, fmt.Errorf("migration catalog contains non-file entry %q", name)
		}

		matches := filenamePattern.FindStringSubmatch(name)
		if matches == nil {
			return Catalog{}, fmt.Errorf("invalid migration filename %q", name)
		}

		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return Catalog{}, fmt.Errorf("invalid migration filename %q", name)
		}
		if version == 0 {
			return Catalog{}, fmt.Errorf("migration version must be positive in %q", name)
		}
		if previous, exists := versions[version]; exists {
			return Catalog{}, fmt.Errorf(
				"migration version %d is duplicated by %q and %q",
				version,
				previous,
				name,
			)
		}

		versions[version] = name
		catalog.Entries = append(catalog.Entries, Entry{Version: version, Name: name})
	}

	sort.Slice(catalog.Entries, func(i, j int) bool {
		return catalog.Entries[i].Version < catalog.Entries[j].Version
	})
	return catalog, nil
}
