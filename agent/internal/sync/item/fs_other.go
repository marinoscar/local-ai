//go:build !linux

package item

// DropCache implements FS; there is no portable equivalent, so it does nothing.
func (OSFS) DropCache(string) error { return nil }

// Exchange implements FS; only Linux has renameat2.
func (OSFS) Exchange(string, string) error { return ErrExchangeUnsupported }
