//go:build !windows

package secret

type unsupportedStore struct{}

func New(_ string) Store { return unsupportedStore{} }
func (unsupportedStore) Save(string, []byte) error   { return ErrUnsupported }
func (unsupportedStore) Load(string) ([]byte, error) { return nil, ErrUnsupported }
func (unsupportedStore) Delete(string) error         { return ErrUnsupported }
