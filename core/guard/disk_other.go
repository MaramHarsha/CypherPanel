//go:build !(linux || darwin)

package guard

// FreeBytes has no implementation on this platform; CheckDiskHeadroom
// propagates ErrUnsupported and the caller degrades to a warning.
func FreeBytes(string) (uint64, error) {
	return 0, ErrUnsupported
}
