// YAML helpers (no YAML library): scalar parse/emit, top-level key/block
// editing. Used for config generation (profile + mixin merge).
package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// KV is one ordered key/value pair. Obj is an insertion-ordered YAML mapping.
type KV struct {
	Key string
	Val interface{}
}

type Obj []KV

func (o Obj) get(key string) (interface{}, bool) {
	for _, kv := range o {
		if kv.Key == key {
			return kv.Val, true
		}
	}
	return nil, false
}

func (o Obj) getString(key string) string {
	if v, ok := o.get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// MarshalJSON preserves insertion order.
func (o Obj) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(kv.Val)
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// UnmarshalJSON preserves object key order (nested objects too).
func (o *Obj) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	v, err := decodeJSONValue(dec)
	if err != nil {
		return err
	}
	obj, ok := v.(Obj)
	if !ok {
		return errNotObject
	}
	*o = obj
	return nil
}

var errNotObject = &jsonError{"not an object"}

type jsonError struct{ s string }

func (e *jsonError) Error() string { return e.s }

func decodeJSONValue(dec *json.Decoder) (interface{}, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); ok {
		switch d {
		case '{':
			var o Obj
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				o = append(o, KV{key, v})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			var arr []interface{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
	}
	return t, nil
}

var (
	topKeyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*):(.*)$`)
	bareRe   = regexp.MustCompile(`(?i)^(true|false|null|~|[-+]?[0-9]+(\.[0-9]+)?)$`)
)

func isTopKeyLine(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == ' ' || c == '\t' || c == '\r' {
		return false
	}
	return !strings.HasPrefix(line, "#")
}

func parseScalar(text string) interface{} {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	if !strings.HasPrefix(t, `"`) && !strings.HasPrefix(t, `'`) {
		if idx := strings.Index(t, " #"); idx >= 0 {
			t = strings.TrimSpace(t[:idx])
		}
	}
	if len(t) >= 2 && strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) {
		var s string
		if err := json.Unmarshal([]byte(t), &s); err == nil {
			return s
		}
		return t[1 : len(t)-1]
	}
	if len(t) >= 2 && strings.HasPrefix(t, `'`) && strings.HasSuffix(t, `'`) {
		return strings.ReplaceAll(t[1:len(t)-1], "''", "'")
	}
	switch t {
	case "true":
		return true
	case "false":
		return false
	case "null", "~":
		return nil
	}
	if i, err := strconv.Atoi(t); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		return f
	}
	return t
}

func quoteYAML(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func yamlStr(s string) string {
	if s == "" {
		return `""`
	}
	if bareRe.MatchString(s) {
		return quoteYAML(s)
	}
	if s != strings.TrimSpace(s) ||
		strings.Contains(s, "\n") ||
		strings.Contains(s, ": ") ||
		strings.Contains(s, " #") ||
		strings.HasSuffix(s, ":") ||
		strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") ||
		strings.ContainsAny(s, "{}[],&*#?|<>='%\"@`") {
		return quoteYAML(s)
	}
	return s
}

func yamlScalar(v interface{}) string {
	if v == nil {
		return "null"
	}
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case string:
		return yamlStr(t)
	case json.Number:
		return t.String()
	}
	return yamlStr(strings.TrimSpace(jsonString(v)))
}

func jsonString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// emitYAML renders an Obj / []interface{} / scalar as YAML lines.
func emitYAML(v interface{}, indent int) []string {
	pad := strings.Repeat(" ", indent)
	switch val := v.(type) {
	case Obj:
		if len(val) == 0 {
			return []string{pad + "{}"}
		}
		var lines []string
		for _, kv := range val {
			switch sub := kv.Val.(type) {
			case Obj:
				if len(sub) > 0 {
					lines = append(lines, pad+kv.Key+":")
					lines = append(lines, emitYAML(sub, indent+2)...)
				} else {
					lines = append(lines, pad+kv.Key+": {}")
				}
			case []interface{}:
				if len(sub) > 0 {
					lines = append(lines, pad+kv.Key+":")
					lines = append(lines, emitYAML(sub, indent+2)...)
				} else {
					lines = append(lines, pad+kv.Key+": []")
				}
			default:
				lines = append(lines, pad+kv.Key+": "+yamlScalar(kv.Val))
			}
		}
		return lines
	case []interface{}:
		if len(val) == 0 {
			return []string{pad + "[]"}
		}
		var lines []string
		for _, item := range val {
			switch sub := item.(type) {
			case Obj:
				if len(sub) > 0 {
					s := emitYAML(sub, indent+2)
					lines = append(lines, pad+"- "+s[0][indent+2:])
					lines = append(lines, s[1:]...)
				} else {
					lines = append(lines, pad+"- {}")
				}
			case []interface{}:
				if len(sub) > 0 {
					s := emitYAML(sub, indent+2)
					lines = append(lines, pad+"- "+s[0][indent+2:])
					lines = append(lines, s[1:]...)
				} else {
					lines = append(lines, pad+"- []")
				}
			default:
				lines = append(lines, pad+"- "+yamlScalar(item))
			}
		}
		return lines
	default:
		return []string{pad + yamlScalar(v)}
	}
}

// getTopValues reads top-level scalar keys from config lines.
// First non-nil occurrence wins.
func getTopValues(lines []string, keys []string) map[string]interface{} {
	vals := map[string]interface{}{}
	for _, k := range keys {
		vals[k] = nil
	}
	for _, ln := range lines {
		if isTopKeyLine(ln) {
			if m := topKeyRe.FindStringSubmatch(ln); m != nil {
				if _, ok := vals[m[1]]; ok && vals[m[1]] == nil {
					vals[m[1]] = parseScalar(m[2])
				}
			}
		}
	}
	return vals
}

// setTopKeys replaces top-level key lines and appends any missing keys at
// the end, in updates order.
// removeTopKeys drops top-level keys (and their block content) from lines.
// Used to drop `port` / `socks-port` when `mixed-port` is set: Mihomo binds
// the mixed server on the same port as the HTTP proxy, so having both set to
// the same value makes the mixed server fail with "address already in use".
func removeTopKeys(lines []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	var out []string
	skip := false
	for _, ln := range lines {
		if isTopKeyLine(ln) {
			if m := topKeyRe.FindStringSubmatch(ln); m != nil {
				skip = drop[m[1]]
				if skip {
					continue
				}
			} else {
				skip = false
			}
		}
		if !skip {
			out = append(out, ln)
		}
	}
	return out
}

func setTopKeys(lines []string, updates []KV) []string {
	var out []string
	seen := map[string]bool{}
	upd := map[string]string{}
	for _, kv := range updates {
		upd[kv.Key] = kv.Val.(string)
	}
	for _, ln := range lines {
		if isTopKeyLine(ln) {
			if m := topKeyRe.FindStringSubmatch(ln); m != nil {
				if nv, ok := upd[m[1]]; ok {
					out = append(out, m[1]+": "+nv)
					seen[m[1]] = true
					continue
				}
			}
		}
		out = append(out, ln)
	}
	for _, kv := range updates {
		if !seen[kv.Key] {
			out = append(out, kv.Key+": "+kv.Val.(string))
		}
	}
	return out
}

type topBlock struct {
	key   string
	start int
	end   int
}

func splitTopBlocks(lines []string) []topBlock {
	var blocks []topBlock
	var cur *topBlock
	for i, ln := range lines {
		if isTopKeyLine(ln) {
			if m := topKeyRe.FindStringSubmatch(ln); m != nil {
				if cur != nil {
					cur.end = i
					blocks = append(blocks, *cur)
				}
				cur = &topBlock{key: m[1], start: i}
			}
		}
	}
	if cur != nil {
		cur.end = len(lines)
		blocks = append(blocks, *cur)
	}
	return blocks
}

// topBlockKeys returns the set of top-level keys present in the lines.
func topBlockKeys(lines []string) map[string]bool {
	keys := map[string]bool{}
	for _, b := range splitTopBlocks(lines) {
		keys[b.key] = true
	}
	return keys
}

// replaceTopBlock replaces (or appends) a whole top-level block identified
// by key with the given lines (first line must be "key:").
func replaceTopBlock(lines []string, key string, block []string) []string {
	for _, b := range splitTopBlocks(lines) {
		if b.key == key {
			out := append([]string{}, lines[:b.start]...)
			out = append(out, block...)
			out = append(out, lines[b.end:]...)
			return out
		}
	}
	out := append([]string{}, lines...)
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, block...)
	return out
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}
