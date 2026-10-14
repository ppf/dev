package tmux

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Sep separates fields in -F output and, followed by a newline, ends each row. tmux prints values raw,
// so a value may contain newlines (except at its start); a value containing Sep makes Parse fail.
const Sep = "\x1f"

const rowEnd = Sep + "\n"

// Query runs a list command (list-panes, list-sessions, ...) with a -F format built from T's
// `tmux:"<format variable>"` tags and parses one T per output line.
func Query[T any](c *Client, args ...string) ([]T, error) {
	names := fieldNames(reflect.TypeFor[T]())
	vars := make([]string, len(names))
	for i, name := range names {
		vars[i] = "#{" + name + "}"
	}
	out, err := c.Run(append(args, "-F", strings.Join(vars, Sep)+Sep)...)
	if err != nil {
		return nil, err
	}
	return Parse[T](out)
}

// FormatRow encodes values as one row of Query output, for fake tmux output in tests.
func FormatRow(values ...string) string {
	return strings.Join(values, Sep) + rowEnd
}

// Parse decodes rows of Sep-joined values, each ending in Sep and a newline, into T, one field per
// `tmux` tag in declaration order.
// Supported field kinds: string, int, bool ("1" is true).
func Parse[T any](out string) ([]T, error) {
	typ := reflect.TypeFor[T]()
	idx := taggedFields(typ)
	if out == "" {
		return nil, nil
	}
	body, ok := strings.CutSuffix(out, rowEnd)
	if !ok {
		return nil, fmt.Errorf("tmux: unterminated row: %q", out)
	}
	var rows []T
	for _, line := range strings.Split(body, rowEnd) {
		values := strings.Split(line, Sep)
		if len(values) != len(idx) {
			return nil, fmt.Errorf("tmux: %d fields, want %d: %q", len(values), len(idx), line)
		}
		var row T
		v := reflect.ValueOf(&row).Elem()
		for i, value := range values {
			if err := setField(v.Field(idx[i]), value); err != nil {
				return nil, fmt.Errorf("tmux: field %s: %w", typ.Field(idx[i]).Tag.Get("tmux"), err)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func fieldNames(typ reflect.Type) []string {
	var names []string
	for _, i := range taggedFields(typ) {
		names = append(names, typ.Field(i).Tag.Get("tmux"))
	}
	return names
}

func taggedFields(typ reflect.Type) []int {
	var idx []int
	for i := range typ.NumField() {
		if typ.Field(i).Tag.Get("tmux") != "" {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		panic(fmt.Sprintf("tmux: %s has no `tmux` tagged fields", typ))
	}
	return idx
}

func setField(f reflect.Value, value string) error {
	switch f.Kind() {
	case reflect.String:
		f.SetString(value)
	case reflect.Int:
		n, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		f.SetInt(int64(n))
	case reflect.Bool:
		f.SetBool(value == "1")
	default:
		panic(fmt.Sprintf("tmux: unsupported field kind %s", f.Kind()))
	}
	return nil
}
