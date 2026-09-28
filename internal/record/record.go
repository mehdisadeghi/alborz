// Package record rewrites stored records keeping fields a newer version added (ADR 26).
package record

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Keep encodes v over old, carrying over, at every depth, fields v's type does not know.
func Keep(old []byte, v any) ([]byte, error) {
	fresh, err := json.Marshal(v)
	if err != nil || len(old) == 0 {
		return fresh, err
	}
	stored, err := decode(old)
	if err != nil {
		// Not an object: replaced.
		return fresh, nil
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	known := reflect.New(t).Interface()
	if err := json.Unmarshal(old, known); err != nil {
		return fresh, nil
	}
	seen, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	understood, err := decode(seen)
	if err != nil {
		return nil, err
	}
	unknown := strip(stored, understood)
	if unknown == nil {
		return fresh, nil
	}
	written, err := decode(fresh)
	if err != nil {
		return nil, err
	}
	return json.Marshal(graft(written, unknown, understood))
}

// decode keeps numbers exact: an id past 2^53 would not survive a float.
func decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// strip returns what stored has that understood does not, or nil.
func strip(stored, understood any) any {
	s, ok := stored.(map[string]any)
	if !ok {
		return nil
	}
	u, _ := understood.(map[string]any)
	out := map[string]any{}
	for key, value := range s {
		known, ok := u[key]
		if !ok {
			out[key] = value
			continue
		}
		if inner := strip(value, known); inner != nil {
			out[key] = inner
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// graft puts unknown back into written. A name this version understood but no
// longer writes, such as a deleted map entry, stays dropped.
func graft(written, unknown, understood any) any {
	w, ok := written.(map[string]any)
	u, isObject := unknown.(map[string]any)
	if !ok || !isObject {
		return written
	}
	known, _ := understood.(map[string]any)
	for key, value := range u {
		if have, ok := w[key]; ok {
			w[key] = graft(have, value, known[key])
		} else if _, dropped := known[key]; !dropped {
			w[key] = value
		}
	}
	return w
}
