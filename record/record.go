// Package record writes a stored record back without losing what a newer
// version of alborz put in it and this one does not know; see ADR 26.
package record

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Keep encodes v for storage over old, the record as it was stored.
// What old has that v's type does not describe - a field a newer
// version added - is carried over as it was, in every nested object; a
// field v's type knows is v's to set or clear.
func Keep(old []byte, v any) ([]byte, error) {
	fresh, err := json.Marshal(v)
	if err != nil || len(old) == 0 {
		return fresh, err
	}
	stored, err := decode(old)
	if err != nil {
		// Not an object this version can read at all; it is replaced,
		// as it would have been before this package existed.
		return fresh, nil
	}
	// What this version makes of the stored record: the fields its type
	// knows, and nothing else. The difference is what it would drop.
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

// decode reads JSON keeping numbers as they were written: a counter or
// an id past 2^53 would not survive a float.
func decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// strip is what stored has that understood does not: whole fields the
// type does not know, and inside fields it knows, what their objects
// have that it does not. Nil when there is nothing.
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

// graft puts unknown back into written, descending into objects both
// have. What written lacks goes back only where this version did not
// understand it at all: a name it understood and no longer writes - an
// entry it deleted from a map - is its to drop, and what a newer
// version kept inside goes with it.
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
