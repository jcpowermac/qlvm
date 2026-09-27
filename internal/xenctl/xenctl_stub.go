//go:build !libxl

package xenctl

import "errors"

// New returns a Xen handle. This build has no libxl binding, so it always
// fails with a rebuild hint; the libxl-tagged implementation provides the
// real xenlight-backed handle.
func New() (Xen, error) {
	return nil, errors.New("qlvm built without Xen support — rebuild with -tags libxl (libxl-devel required)")
}
