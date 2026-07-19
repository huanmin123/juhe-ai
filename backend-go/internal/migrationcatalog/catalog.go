package migrationcatalog

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var filenamePattern = regexp.MustCompile(`^([0-9]{6})_[a-z0-9_]+\.sql$`)

const CurrentSchemaVersion int64 = 62

const (
	annotationUp             = "Up"
	annotationDown           = "Down"
	annotationStatementBegin = "StatementBegin"
	annotationStatementEnd   = "StatementEnd"
)

type Entry struct {
	Version int64
	Name    string
}

type Catalog struct {
	Entries []Entry
}

type sqlLexState struct {
	atStatementStart       bool
	blockCommentDepth      int
	singleQuoted           bool
	singleBackslashEscapes bool
	doubleQuoted           bool
	dollarDelimiter        string
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
	for _, entry := range catalog.Entries {
		if err := inspectSQL(fsys, entry.Name); err != nil {
			return Catalog{}, err
		}
	}
	return catalog, nil
}

func inspectSQL(fsys fs.FS, name string) (resultErr error) {
	file, err := fsys.Open(name)
	if err != nil {
		return fmt.Errorf("migration %q is unavailable", name)
	}
	defer func() {
		if err := file.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("migration %q could not be closed", name)
		}
	}()

	insideStatement := false
	section := ""
	lexState := sqlLexState{atStatementStart: true}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		annotation, found, err := parseGooseAnnotation(line)
		if err != nil {
			return fmt.Errorf("migration %q has invalid goose annotation at line %d", name, lineNumber)
		}
		if found {
			switch annotation {
			case annotationUp:
				if section != "" || insideStatement {
					return fmt.Errorf("migration %q has misplaced goose Up at line %d", name, lineNumber)
				}
				section = annotationUp
			case annotationDown:
				if section != annotationUp || insideStatement {
					return fmt.Errorf("migration %q has misplaced goose Down at line %d", name, lineNumber)
				}
				section = annotationDown
			case annotationStatementBegin:
				if section == "" || insideStatement {
					return fmt.Errorf("migration %q has misplaced goose StatementBegin at line %d", name, lineNumber)
				}
				insideStatement = true
			case annotationStatementEnd:
				if !insideStatement {
					return fmt.Errorf("migration %q has unmatched goose StatementEnd at line %d", name, lineNumber)
				}
				insideStatement = false
				lexState.atStatementStart = true
			}
			continue
		}

		if !insideStatement {
			if err := inspectSQLLine(name, line, lineNumber, &lexState); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("migration %q could not be read", name)
	}
	if insideStatement {
		return fmt.Errorf("migration %q has unmatched goose StatementBegin", name)
	}
	return nil
}

func inspectSQLLine(name, line string, lineNumber int, state *sqlLexState) error {
	for index := 0; index < len(line); {
		if state.blockCommentDepth > 0 {
			switch {
			case strings.HasPrefix(line[index:], "/*"):
				state.blockCommentDepth++
				index += 2
			case strings.HasPrefix(line[index:], "*/"):
				state.blockCommentDepth--
				index += 2
			default:
				index++
			}
			continue
		}
		if state.dollarDelimiter != "" {
			end := strings.Index(line[index:], state.dollarDelimiter)
			if end < 0 {
				return nil
			}
			index += end + len(state.dollarDelimiter)
			state.dollarDelimiter = ""
			continue
		}
		if state.singleQuoted {
			switch {
			case state.singleBackslashEscapes && line[index] == '\\' && index+1 < len(line):
				index += 2
			case line[index] == '\'' && index+1 < len(line) && line[index+1] == '\'':
				index += 2
			case line[index] == '\'':
				state.singleQuoted = false
				state.singleBackslashEscapes = false
				index++
			default:
				index++
			}
			continue
		}
		if state.doubleQuoted {
			if line[index] == '"' {
				if index+1 < len(line) && line[index+1] == '"' {
					index += 2
					continue
				}
				state.doubleQuoted = false
			}
			index++
			continue
		}

		switch {
		case strings.HasPrefix(line[index:], "--"):
			return nil
		case strings.HasPrefix(line[index:], "/*"):
			state.blockCommentDepth = 1
			index += 2
		case (line[index] == 'E' || line[index] == 'e') && index+1 < len(line) && line[index+1] == '\'':
			state.singleQuoted = true
			state.singleBackslashEscapes = true
			state.atStatementStart = false
			index += 2
		case line[index] == '\'':
			state.singleQuoted = true
			state.atStatementStart = false
			index++
		case line[index] == '"':
			state.doubleQuoted = true
			state.atStatementStart = false
			index++
		case line[index] == '$':
			if delimiter, ok := dollarDelimiterAt(line, index); ok {
				state.dollarDelimiter = delimiter
				state.atStatementStart = false
				index += len(delimiter)
			} else {
				state.atStatementStart = false
				index++
			}
		case line[index] == ';':
			state.atStatementStart = true
			index++
		case isSQLSpace(line[index]):
			index++
		case isIdentifierStart(line[index]):
			end := index + 1
			for end < len(line) && isIdentifierPart(line[end]) {
				end++
			}
			if state.atStatementStart && strings.EqualFold(line[index:end], "DO") {
				return fmt.Errorf(
					"migration %q contains procedural DO outside goose StatementBegin at line %d",
					name,
					lineNumber,
				)
			}
			state.atStatementStart = false
			index = end
		default:
			state.atStatementStart = false
			index++
		}
	}
	return nil
}

func dollarDelimiterAt(line string, index int) (string, bool) {
	if index+1 >= len(line) {
		return "", false
	}
	if line[index+1] == '$' {
		return "$$", true
	}
	if !isIdentifierStart(line[index+1]) {
		return "", false
	}
	end := index + 2
	for end < len(line) && isDollarTagPart(line[end]) {
		end++
	}
	if end >= len(line) || line[end] != '$' {
		return "", false
	}
	return line[index : end+1], true
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isIdentifierPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9' || value == '$'
}

func isDollarTagPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9'
}

func isSQLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}

func parseGooseAnnotation(line string) (string, bool, error) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "--") || !strings.Contains(line, "+goose") {
		return "", false, nil
	}
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", true, errors.New("goose annotation contains leading whitespace")
	}

	command := strings.ReplaceAll(line, "--", "")
	command = strings.Replace(command, "+goose", "", 1)
	if strings.Contains(command, "+goose") {
		return "", true, errors.New("goose annotation occurs more than once")
	}
	command = strings.TrimSpace(command)
	for _, supported := range []string{
		annotationUp,
		annotationDown,
		annotationStatementBegin,
		annotationStatementEnd,
		"NO TRANSACTION",
		"ENVSUB ON",
		"ENVSUB OFF",
	} {
		if strings.EqualFold(command, supported) {
			return supported, true, nil
		}
	}
	return "", true, errors.New("unsupported goose annotation")
}
