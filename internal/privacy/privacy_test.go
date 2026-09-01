package privacy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

func TestAgentDataNeverContainsPathsOrNames(t *testing.T) {
	secret := `C:\Users\alice\.ssh\id_rsa`
	scan := model.Scan{ID: "s", EndedAt: time.Now(), Entries: []model.Entry{{ID: "e", RootID: "r", Path: secret, Relative: `.ssh/id_rsa`, Name: "id_rsa", Kind: model.KindFile}}}
	encoded, err := json.Marshal(Isolate(scan))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"alice", ".ssh", "id_rsa", `C:\\Users`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("agent data leaked %q: %s", forbidden, text)
		}
	}
}
