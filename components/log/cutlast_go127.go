//go:build go1.27

package log

import "strings"

func cutLast(s, sep string) (before, after string, found bool) {
	return strings.CutLast(s, sep)
}
