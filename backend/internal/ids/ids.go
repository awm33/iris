// Package ids generates type-prefixed ULIDs (ws_, prj_, ast_, ...) — the ID
// convention documented in proto/iris/v1/common.proto.
package ids

import (
	"crypto/rand"
	"strings"

	"github.com/oklog/ulid/v2"
)

func New(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String())
}
