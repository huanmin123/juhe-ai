package managementstats

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
)

type nodeLegacyTimezoneAlias struct {
	location    string
	offsetHours int
	fixed       bool
}

var rootTimezoneNames = map[string]string{
	"cet":       "CET",
	"cst6cdt":   "CST6CDT",
	"cuba":      "Cuba",
	"eet":       "EET",
	"egypt":     "Egypt",
	"eire":      "Eire",
	"est":       "EST",
	"est5edt":   "EST5EDT",
	"factory":   "Factory",
	"gb":        "GB",
	"gb-eire":   "GB-Eire",
	"gmt":       "GMT",
	"gmt-0":     "GMT-0",
	"gmt+0":     "GMT+0",
	"gmt0":      "GMT0",
	"greenwich": "Greenwich",
	"hongkong":  "Hongkong",
	"hst":       "HST",
	"iceland":   "Iceland",
	"iran":      "Iran",
	"israel":    "Israel",
	"jamaica":   "Jamaica",
	"japan":     "Japan",
	"kwajalein": "Kwajalein",
	"libya":     "Libya",
	"met":       "MET",
	"mst":       "MST",
	"mst7mdt":   "MST7MDT",
	"navajo":    "Navajo",
	"nz":        "NZ",
	"nz-chat":   "NZ-CHAT",
	"poland":    "Poland",
	"portugal":  "Portugal",
	"prc":       "PRC",
	"pst8pdt":   "PST8PDT",
	"roc":       "ROC",
	"rok":       "ROK",
	"singapore": "Singapore",
	"turkey":    "Turkey",
	"uct":       "UCT",
	"universal": "Universal",
	"utc":       "UTC",
	"w-su":      "W-SU",
	"wet":       "WET",
	"zulu":      "Zulu",
}

var timezoneRegionNames = map[string]string{
	"africa":     "Africa",
	"america":    "America",
	"antarctica": "Antarctica",
	"arctic":     "Arctic",
	"asia":       "Asia",
	"atlantic":   "Atlantic",
	"australia":  "Australia",
	"brazil":     "Brazil",
	"canada":     "Canada",
	"chile":      "Chile",
	"etc":        "Etc",
	"europe":     "Europe",
	"indian":     "Indian",
	"mexico":     "Mexico",
	"pacific":    "Pacific",
	"us":         "US",
}

var exceptionalTimezoneNames = map[string]string{
	"africa/dar_es_salaam":             "Africa/Dar_es_Salaam",
	"america/argentina/comodrivadavia": "America/Argentina/ComodRivadavia",
	"america/knox_in":                  "America/Knox_IN",
	"america/port-au-prince":           "America/Port-au-Prince",
	"antarctica/dumontdurville":        "Antarctica/DumontDUrville",
	"antarctica/mcmurdo":               "Antarctica/McMurdo",
	"australia/act":                    "Australia/ACT",
	"australia/lhi":                    "Australia/LHI",
	"australia/nsw":                    "Australia/NSW",
	"brazil/denoronha":                 "Brazil/DeNoronha",
	"chile/easterisland":               "Chile/EasterIsland",
	"etc/gmt0":                         "Etc/GMT0",
	"etc/uct":                          "Etc/UCT",
	"etc/utc":                          "Etc/UTC",
	"mexico/bajanorte":                 "Mexico/BajaNorte",
	"mexico/bajasur":                   "Mexico/BajaSur",
}

var nodeLegacyTimezoneAliases = map[string]nodeLegacyTimezoneAlias{
	"us/pacific-new":           {location: "America/Los_Angeles"},
	"canada/east-saskatchewan": {location: "America/Regina"},
	"systemv/ast4":             {offsetHours: -4, fixed: true},
	"systemv/ast4adt":          {location: "America/Halifax"},
	"systemv/cst6":             {offsetHours: -6, fixed: true},
	"systemv/cst6cdt":          {location: "America/Chicago"},
	"systemv/est5":             {offsetHours: -5, fixed: true},
	"systemv/est5edt":          {location: "America/New_York"},
	"systemv/hst10":            {offsetHours: -10, fixed: true},
	"systemv/mst7":             {offsetHours: -7, fixed: true},
	"systemv/mst7mdt":          {location: "America/Denver"},
	"systemv/pst8":             {offsetHours: -8, fixed: true},
	"systemv/pst8pdt":          {location: "America/Los_Angeles"},
	"systemv/yst9":             {offsetHours: -9, fixed: true},
	"systemv/yst9ydt":          {location: "America/Anchorage"},
}

func loadUsageStatsLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if lower == "local" || lower == "factory" {
		return nil, fmt.Errorf("unknown time zone %s", name)
	}
	if alias, ok := nodeLegacyTimezoneAliases[lower]; ok {
		if alias.fixed {
			return time.FixedZone(name, alias.offsetHours*60*60), nil
		}
		return time.LoadLocation(alias.location)
	}
	if canonical := canonicalIANATimezoneName(name); canonical != "" {
		return time.LoadLocation(canonical)
	}
	return nil, fmt.Errorf("unknown time zone %s", name)
}

func canonicalIANATimezoneName(name string) string {
	name = strings.TrimSpace(name)
	if !validIANATimezoneName(name) {
		return ""
	}
	lower := strings.ToLower(name)
	if canonical := rootTimezoneNames[lower]; canonical != "" {
		return canonical
	}
	if canonical := exceptionalTimezoneNames[lower]; canonical != "" {
		return canonical
	}
	parts := strings.Split(lower, "/")
	if len(parts) < 2 {
		return ""
	}
	region := timezoneRegionNames[parts[0]]
	if region == "" {
		return ""
	}
	canonical := make([]string, 0, len(parts))
	canonical = append(canonical, region)
	for _, part := range parts[1:] {
		canonical = append(canonical, canonicalTimezonePart(part))
	}
	return strings.Join(canonical, "/")
}

func canonicalTimezonePart(part string) string {
	if part == "gmt" {
		return "GMT"
	}
	if strings.HasPrefix(part, "gmt+") || strings.HasPrefix(part, "gmt-") {
		return "GMT" + part[3:]
	}
	var out strings.Builder
	upperNext := true
	for index := 0; index < len(part); index++ {
		char := part[index]
		switch char {
		case '_', '-':
			out.WriteByte(char)
			upperNext = true
		default:
			if upperNext {
				out.WriteString(strings.ToUpper(string(char)))
				upperNext = false
			} else {
				out.WriteByte(char)
			}
		}
	}
	return strings.ReplaceAll(out.String(), "_Of_", "_of_")
}

func validIANATimezoneName(name string) bool {
	return name != "" &&
		!strings.HasPrefix(name, "/") &&
		!strings.Contains(name, "\\") &&
		!strings.Contains(name, "..")
}
