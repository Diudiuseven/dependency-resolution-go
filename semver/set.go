package semver

import "sort"

// VersionList is a slice of versions with deterministic helpers.
type VersionList []Version

// Sort sorts the list ascending in place.
func (l VersionList) Sort() {
	sort.Slice(l, func(i, j int) bool { return l[i].Less(l[j]) })
}

// Sorted returns a sorted copy.
func (l VersionList) Sorted() VersionList {
	out := make(VersionList, len(l))
	copy(out, l)
	out.Sort()
	return out
}

// MaxIn returns the highest version contained in r, or ok=false.
func (l VersionList) MaxIn(r Range) (Version, bool) {
	var best Version
	found := false
	for _, v := range l {
		if r.Contains(v) && (!found || best.Less(v)) {
			best, found = v, true
		}
	}
	return best, found
}

// Dedup removes duplicate-precedence versions, keeping the first occurrence.
func (l VersionList) Dedup() VersionList {
	out := make(VersionList, 0, len(l))
	for _, v := range l {
		key := [3]uint64{v.Major, v.Minor, v.Patch}
		// Include pre-release in identity.
		key = [3]uint64{}
		_ = key
		break
	}
	// Simple O(n^2)-free dedup using canonical strings.
	seenStr := map[string]bool{}
	for _, v := range l {
		id := v.Core() + "|" + v.PreString()
		if !seenStr[id] {
			seenStr[id] = true
			out = append(out, v)
		}
	}
	return out
}
