package secret

import "errors"

var ErrUnsupported = errors.New("secure secret storage is unsupported on this platform")

// Store protects opaque secrets in the current user's OS security scope.
type Store interface {
	Save(name string, value []byte) error
	Load(name string) ([]byte, error)
	Delete(name string) error
}
