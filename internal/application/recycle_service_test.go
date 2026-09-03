package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/pathsafe"
	"github.com/kyfd/qijing/internal/store"
)

// seedRecycleScan authorizes root and installs a snapshot describing the given
// files, so recycle flows can be exercised without running a real scan.
func seedRecycleScan(t *testing.T, service *Service, root string, paths ...string) model.Scan {
	t.Helper()
	if _, err := service.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	scan := model.Scan{ID: "snapshot-1", Roots: []string{root}, StartedAt: time.Now(), EndedAt: time.Now()}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		scan.Entries = append(scan.Entries, model.Entry{
			ID:      "entry-" + filepath.Base(path),
			Name:    filepath.Base(path),
			Path:    path,
			Kind:    model.KindFile,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Classes: []model.Class{model.ClassRotten},
		})
	}
	installScan(t, service, scan)
	return scan
}

// installScan persists the snapshot and leaves the manager holding metadata
// only, matching production: entries are read back from SQLite on demand.
// Each call needs a distinct scan id, since a snapshot is written once.
func installScan(t *testing.T, service *Service, scan model.Scan) {
	t.Helper()
	if err := service.db.SaveScan(context.Background(), scan); err != nil {
		t.Fatal(err)
	}
	meta := scan
	meta.Entries = nil
	meta.Relations = nil
	service.manager.mu.Lock()
	service.manager.latest = meta
	service.manager.mu.Unlock()
}

func newRecycleService(t *testing.T) *Service {
	t.Helper()
	service, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeService(t, service) })
	return service
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func recycledFixture() store.RecycledItem {
	return store.RecycledItem{
		ID: "audit-1", ScanID: "snapshot-gone", EntryID: "entry-1",
		Path: filepath.Join("C:\\", "tmp", "cache.tmp"), Name: "cache.tmp", Kind: "file",
		Size: 4, Root: "C:\\tmp", ConfirmedAt: time.Now(), RecycledAt: time.Now(),
		Outcome: store.RecycleOutcomeRecycled,
	}
}

func TestRecycleCandidatesOnlyOffersDisposableClasses(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	rotten := writeFile(t, root, "cache.tmp", "junk")
	keeper := writeFile(t, root, "thesis.docx", "important")
	scan := seedRecycleScan(t, service, root, rotten, keeper)
	// The second entry is a merely large, dormant file: never a candidate.
	scan.Entries[1].Classes = []model.Class{model.ClassGiant, model.ClassDormant}
	scan.ID = "snapshot-classes"
	installScan(t, service, scan)

	candidates := service.RecycleCandidates(context.Background())
	if len(candidates.Candidates) != 1 || candidates.Candidates[0].Path != rotten {
		t.Fatalf("candidates=%#v", candidates.Candidates)
	}
	if !candidates.Candidates[0].Eligible {
		t.Fatalf("expected the rotten file to be eligible: %#v", candidates.Candidates[0])
	}
}

func TestRecyclePathValidationRejectsUnsafeTargets(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	inside := writeFile(t, root, "cache.tmp", "junk")
	seedRecycleScan(t, service, root, inside)
	outside := writeFile(t, t.TempDir(), "elsewhere.tmp", "junk")

	if _, err := service.validateRecyclePath(outside); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outside root err=%v", err)
	}
	if _, err := service.validateRecyclePath(root); err == nil {
		t.Fatal("expected an authorized root itself to be refused")
	}
	if _, err := service.validateRecyclePath(filepath.Join(root, "sub")); err == nil {
		t.Fatal("expected a missing path to be refused")
	}
	dir := filepath.Join(root, "nested")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.validateRecyclePath(dir); err == nil {
		t.Fatal("expected a directory to be refused")
	}
	if _, err := service.validateRecyclePath("cache.tmp"); err == nil {
		t.Fatal("expected a relative path to be refused")
	}
	if _, err := service.validateRecyclePath(inside); err != nil {
		t.Fatalf("a regular file inside the root should validate: %v", err)
	}
}

func TestRecyclePathValidationRejectsSymlinkedComponents(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	secret := writeFile(t, t.TempDir(), "secret.txt", "sensitive")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	seedRecycleScan(t, service, root, secret)

	_, err := service.validateRecyclePath(link)
	if err == nil {
		t.Fatal("expected a symlinked path to be refused")
	}
	if !errors.Is(err, pathsafe.ErrSymlink) && !errors.Is(err, pathsafe.ErrReparse) {
		// Lstat on the link itself is also non-regular, which is equally safe.
		if _, statErr := os.Lstat(link); statErr != nil {
			t.Fatalf("unexpected refusal reason: %v", err)
		}
	}
	if _, statErr := os.Lstat(secret); statErr != nil {
		t.Fatalf("the link target must remain untouched: %v", statErr)
	}
}

func TestRecycleConfirmationIsSingleUseAndBound(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	target := writeFile(t, root, "cache.tmp", "junk")
	scan := seedRecycleScan(t, service, root, target)
	ctx := context.Background()

	if _, err := service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{}); !errors.Is(err, ErrRecycleEmpty) {
		t.Fatalf("empty selection err=%v", err)
	}
	if _, err := service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{EntryIDs: []string{"nope"}}); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("unknown entry err=%v", err)
	}
	tooMany := make([]string, maxRecycleBatch+1)
	for i := range tooMany {
		tooMany[i] = scan.Entries[0].ID
	}
	if _, err := service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{EntryIDs: tooMany}); err == nil {
		t.Fatal("expected an oversized batch to be refused")
	}

	preview, err := service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{EntryIDs: []string{scan.Entries[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.SelectionHash == "" || preview.ConfirmationToken == "" || len(preview.Items) != 1 {
		t.Fatalf("preview=%#v", preview)
	}

	// A mismatched hash must not consume nor authorize the token.
	if _, err = service.ConfirmRecycle(ctx, RecycleConfirmRequestDTO{SelectionHash: "wrong", ConfirmationToken: preview.ConfirmationToken}); !errors.Is(err, ErrRecycleConfirmation) {
		t.Fatalf("mismatched hash err=%v", err)
	}
	// The token was consumed by that attempt, so a replay must also fail.
	if _, err = service.ConfirmRecycle(ctx, RecycleConfirmRequestDTO{SelectionHash: preview.SelectionHash, ConfirmationToken: preview.ConfirmationToken}); !errors.Is(err, ErrRecycleConfirmation) {
		t.Fatalf("replayed token err=%v", err)
	}
	if _, err = os.Lstat(target); err != nil {
		t.Fatalf("no confirmation succeeded, the file must remain: %v", err)
	}
}

func TestRecycleConfirmationExpiresAndIsSnapshotBound(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	target := writeFile(t, root, "cache.tmp", "junk")
	scan := seedRecycleScan(t, service, root, target)
	ctx := context.Background()

	preview, err := service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{EntryIDs: []string{scan.Entries[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	service.recycle.mu.Lock()
	confirmation := service.recycle.confirmations[preview.ConfirmationToken]
	confirmation.expires = time.Now().Add(-time.Second)
	service.recycle.confirmations[preview.ConfirmationToken] = confirmation
	service.recycle.mu.Unlock()
	if _, err = service.ConfirmRecycle(ctx, RecycleConfirmRequestDTO{SelectionHash: preview.SelectionHash, ConfirmationToken: preview.ConfirmationToken}); !errors.Is(err, ErrRecycleConfirmation) {
		t.Fatalf("expired token err=%v", err)
	}

	preview, err = service.PreviewRecycle(ctx, RecyclePreviewRequestDTO{EntryIDs: []string{scan.Entries[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	// A new scan replaces the snapshot the confirmation was bound to.
	service.manager.mu.Lock()
	service.manager.latest.ID = "snapshot-2"
	service.manager.mu.Unlock()
	if _, err = service.ConfirmRecycle(ctx, RecycleConfirmRequestDTO{SelectionHash: preview.SelectionHash, ConfirmationToken: preview.ConfirmationToken}); !errors.Is(err, ErrRecycleConfirmation) {
		t.Fatalf("cross-snapshot token err=%v", err)
	}
	if _, err = os.Lstat(target); err != nil {
		t.Fatalf("the file must remain: %v", err)
	}
}

func TestRecycleRefusesFilesChangedSincePreview(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	target := writeFile(t, root, "cache.tmp", "junk")
	scan := seedRecycleScan(t, service, root, target)

	preview, err := service.PreviewRecycle(context.Background(), RecyclePreviewRequestDTO{EntryIDs: []string{scan.Entries[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	service.recycle.mu.Lock()
	targets := service.recycle.confirmations[preview.ConfirmationToken].targets
	service.recycle.mu.Unlock()
	if len(targets) != 1 {
		t.Fatalf("targets=%#v", targets)
	}
	// The user consented to a file of a particular size and mtime; anything
	// else is a different file and must not be acted on.
	if err = os.WriteFile(target, []byte("the user just saved real work here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = service.recycleOne(targets[0]); !errors.Is(err, ErrRecycleChanged) {
		t.Fatalf("changed file err=%v", err)
	}
	if _, err = os.Lstat(target); err != nil {
		t.Fatalf("the changed file must remain: %v", err)
	}
}

// The strongest TOCTOU scenario: the file at the confirmed path is replaced
// by a different file object whose size and modification time are identical
// to the previewed ones. Stat-data checks alone would approve this; the
// Windows file identity must not.
func TestRecycleRefusesFileReplacedWithIdenticalStatData(t *testing.T) {
	service := newRecycleService(t)
	root := t.TempDir()
	target := writeFile(t, root, "cache.tmp", "junk")
	scan := seedRecycleScan(t, service, root, target)

	preview, err := service.PreviewRecycle(context.Background(), RecyclePreviewRequestDTO{EntryIDs: []string{scan.Entries[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	service.recycle.mu.Lock()
	targets := service.recycle.confirmations[preview.ConfirmationToken].targets
	service.recycle.mu.Unlock()

	original, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the file: same name, same size, and the modification time is
	// forced back to the observed value.
	if err = os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(target, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}

	if err = service.recycleOne(targets[0]); !errors.Is(err, ErrRecycleChanged) {
		t.Fatalf("a replaced file must be refused, got %v", err)
	}
	if _, err = os.Lstat(target); err != nil {
		t.Fatalf("the replaced file must remain: %v", err)
	}
}

func TestRecycleHistoryStartsEmptyAndSurvivesSnapshotLoss(t *testing.T) {
	service := newRecycleService(t)
	ctx := context.Background()
	history, err := service.RecycleHistory(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 0 {
		t.Fatalf("history=%#v", history.Items)
	}
	// The audit table deliberately has no foreign key onto scans, so a record
	// outlives the snapshot that produced it.
	if err = service.db.AddRecycledItem(ctx, recycledFixture()); err != nil {
		t.Fatal(err)
	}
	history, err = service.RecycleHistory(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].Path == "" || history.Items[0].Outcome == "" {
		t.Fatalf("history=%#v", history.Items)
	}
}

func TestPrivacyReportsRecycleCapabilityHonestly(t *testing.T) {
	service := newRecycleService(t)
	privacy := service.Privacy(context.Background())
	caps := privacy.Capabilities
	if caps.PermanentDelete || caps.MoveOrRename || caps.FileContentRewrite || caps.ArbitraryShell {
		t.Fatalf("capabilities overstate write powers: %#v", caps)
	}
	if caps.RecycleBin == "" {
		t.Fatal("the recycle capability must be disclosed, not left blank")
	}
}
