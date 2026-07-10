package managementstats

import (
	"archive/zip"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCanonicalIANATimezoneNameCoversBundledZoneinfo(t *testing.T) {
	archive, err := zip.OpenReader(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Skipf("bundled zoneinfo unavailable: %v", err)
	}
	defer archive.Close()

	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := entry.Name
		t.Run(name, func(t *testing.T) {
			if got := canonicalIANATimezoneName(strings.ToLower(name)); got != name {
				t.Fatalf("canonicalIANATimezoneName(%q) = %q, want %q", strings.ToLower(name), got, name)
			}
		})
	}
}

func TestLoadUsageStatsLocationSupportsNodeLegacyAliases(t *testing.T) {
	tests := []struct {
		name         string
		winterOffset int
		summerOffset int
	}{
		{name: "US/Pacific-New", winterOffset: -8, summerOffset: -7},
		{name: "Canada/East-Saskatchewan", winterOffset: -6, summerOffset: -6},
		{name: "SystemV/AST4", winterOffset: -4, summerOffset: -4},
		{name: "SystemV/AST4ADT", winterOffset: -4, summerOffset: -3},
		{name: "SystemV/CST6", winterOffset: -6, summerOffset: -6},
		{name: "SystemV/CST6CDT", winterOffset: -6, summerOffset: -5},
		{name: "SystemV/EST5", winterOffset: -5, summerOffset: -5},
		{name: "SystemV/EST5EDT", winterOffset: -5, summerOffset: -4},
		{name: "SystemV/HST10", winterOffset: -10, summerOffset: -10},
		{name: "SystemV/MST7", winterOffset: -7, summerOffset: -7},
		{name: "SystemV/MST7MDT", winterOffset: -7, summerOffset: -6},
		{name: "SystemV/PST8", winterOffset: -8, summerOffset: -8},
		{name: "SystemV/PST8PDT", winterOffset: -8, summerOffset: -7},
		{name: "SystemV/YST9", winterOffset: -9, summerOffset: -9},
		{name: "SystemV/YST9YDT", winterOffset: -9, summerOffset: -8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location, err := loadUsageStatsLocation(strings.ToLower(test.name))
			if err != nil {
				t.Fatalf("loadUsageStatsLocation(%q) error = %v", test.name, err)
			}
			for _, item := range []struct {
				at         time.Time
				wantOffset int
			}{
				{at: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), wantOffset: test.winterOffset},
				{at: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), wantOffset: test.summerOffset},
			} {
				_, offset := item.at.In(location).Zone()
				if offset != item.wantOffset*60*60 {
					t.Fatalf("offset at %s = %d, want %d hours", item.at, offset, item.wantOffset)
				}
			}
		})
	}
}

func TestLoadUsageStatsLocationRejectsGoOnlySpecialNames(t *testing.T) {
	for _, name := range []string{"Local", "local", "Factory", "factory"} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadUsageStatsLocation(name); err == nil {
				t.Fatalf("loadUsageStatsLocation(%q) error = nil, want Node Intl rejection", name)
			}
		})
	}
}
