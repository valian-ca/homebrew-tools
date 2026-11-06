package adopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// object is a JSON object that keeps its keys in file order, so a merged
// settings file changes only where lanes init adds to it.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *object) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after the top-level value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, fmt.Errorf("unexpected %q", t)
	default:
		return tok, nil
	}
}

func encodePretty(v any) []byte {
	var b strings.Builder
	writePretty(&b, v, "")
	b.WriteByte('\n')
	return []byte(b.String())
}

func writePretty(b *strings.Builder, v any, indent string) {
	inner := indent + "  "
	switch t := v.(type) {
	case *object:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(inner)
			b.WriteString(scalarJSON(k))
			b.WriteString(": ")
			writePretty(b, t.vals[k], inner)
			if i < len(t.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(inner)
			writePretty(b, e, inner)
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	default:
		b.WriteString(scalarJSON(t))
	}
}

// encodeCompact writes the contract's one-line form: { "k": v } and [a, b].
func encodeCompact(v any) string {
	switch t := v.(type) {
	case *object:
		if len(t.keys) == 0 {
			return "{}"
		}
		parts := make([]string, len(t.keys))
		for i, k := range t.keys {
			parts[i] = scalarJSON(k) + ": " + encodeCompact(t.vals[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = encodeCompact(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return scalarJSON(t)
	}
}

// scalarJSON leaves &, < and > unescaped: hook commands are shell.
func scalarJSON(v any) string {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(buf.String(), "\n")
}
