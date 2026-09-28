package dav

// Privileges is the DAV:current-user-privilege-set; it says whether a collection is writable.
type Privileges []struct {
	Write        *struct{} `xml:"DAV: write"`
	WriteContent *struct{} `xml:"DAV: write-content"`
	Bind         *struct{} `xml:"DAV: bind"`
}

// Writable reports whether a granted privilege allows writing, and whether the server said at all.
func (ps Privileges) Writable() (writable, known bool) {
	for _, p := range ps {
		known = true
		if p.Write != nil || p.WriteContent != nil || p.Bind != nil {
			writable = true
		}
	}
	return writable, known
}
