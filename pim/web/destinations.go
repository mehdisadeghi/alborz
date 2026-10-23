package web

import "git.mehdix.org/alborz/pim/dav"

// VisibleDestinations keeps writable collections in the reader's standing choice.
// A temporary URL filter does not change where a new object may be filed.
func VisibleDestinations(collections []dav.Collection) []dav.Group {
	var groups []dav.Group
	indexes := make(map[string]int)
	for _, collection := range collections {
		if !collection.Visible || !collection.Writable {
			continue
		}
		at, ok := indexes[collection.Account]
		if !ok {
			at = len(groups)
			indexes[collection.Account] = at
			groups = append(groups, dav.Group{Account: collection.Account})
		}
		groups[at].Collections = append(groups[at].Collections, collection)
	}
	return groups
}
