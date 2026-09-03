package dav

// Labels are what a pooled list's row says about the collection holding
// its object: the collection itself, the zero one when none holds it.
func Labels(colls []Collection) func(account, path string) Collection {
	return func(account, path string) Collection {
		if c := Holding(colls, account, path); c != nil {
			return *c
		}
		return Collection{}
	}
}
