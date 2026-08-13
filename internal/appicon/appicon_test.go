package appicon

import (
	"bytes"
	"testing"

	"github.com/tc-hib/winres"
)

func TestICOIsEmbeddedAndMultiResolution(t *testing.T) {
	if len(ICO) < 4 || !bytes.Equal(ICO[:4], []byte{0, 0, 1, 0}) {
		t.Fatal("embedded application icon is not an ICO")
	}
	icon, err := winres.LoadICO(bytes.NewReader(ICO))
	if err != nil {
		t.Fatalf("LoadICO() error = %v", err)
	}
	if icon == nil {
		t.Fatal("LoadICO() returned nil")
	}
}
