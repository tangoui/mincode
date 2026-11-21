//go:build !darwin && !windows

package misc

import "hash/fnv"

// Without a change counter the text itself is hashed; each poll spawns the
// clipboard tool, so callers poll at most about once a second.
func clipboardState() (int64, bool, error) {
	text, err := clipboardRead()
	if err != nil {
		return 0, false, err
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(text))
	return int64(hash.Sum64() & (1<<62 - 1)), false, nil
}
