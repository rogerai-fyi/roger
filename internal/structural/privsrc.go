package structural

import (
	"os"
	"path/filepath"
)

// BrokerDir is the broker's source directory, relative to the module root. It exists in the
// private source of truth and is left out of the public export (scripts/public-export.sh), so
// its presence is how a test tells which of the two trees it is running in.
const BrokerDir = "cmd/rogerai-broker"

// PrivateSourceAbsent reports whether the tree rooted at root is the public export: the broker
// source is not there. A test that needs the broker (to build it, read it, or find the code
// that implements a spec) skips in the public export and still runs in the private repo.
func PrivateSourceAbsent(root string) bool {
	_, err := os.Stat(filepath.Join(root, BrokerDir))
	return os.IsNotExist(err)
}
