package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/pathsafe"
	"github.com/kyfd/qijing/internal/platform"
	"github.com/kyfd/qijing/internal/privacy"
	"github.com/kyfd/qijing/internal/scanbroker"
	"github.com/kyfd/qijing/internal/store"
)

var ErrNodeNotFound = errors.New("node not found")
var ErrUnauthorized = errors.New("path is no longer authorized")

// ScanEngine is the transport-independent scan capability the application
// layer drives. Production engines run in the scanner subprocess; tests may
// use the in-process scanner.
type ScanEngine interface {
	Scan(ctx context.Context) (model.Scan, error)
}

// ScanEngineFactory creates one engine for one validated configuration.
type ScanEngineFactory func(cfg config.Config) (ScanEngine, error)

type Options struct {
	DataDir string
	Context context.Context
	Model   ModelClient
	Secrets SecretStore
	// ScanFactory overrides how scan engines are created. Production leaves
	// it nil so scans run in the independent qijing-scanner subprocess via
	// scanbroker; tests inject the in-process engine.
	ScanFactory ScanEngineFactory
	// OnStartupCleanup receives the number of leftover staging snapshots
	// purged during startup; production forwards it to the local log.
	OnStartupCleanup func(count int)
}

// Service is the transport- and desktop-independent application boundary.
type Service struct {
	mu      sync.RWMutex
	db      *store.Store
	cfg     config.Config
	manager *ScanManager
	agent   *AgentManager
	recycle *RecycleManager
}

func New(options Options) (*Service, error) {
	if err := os.MkdirAll(options.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := store.Open(filepath.Join(options.DataDir, "ecosystem.db"))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	// No scan survives a process restart: leftover staging snapshots from a
	// crash or hard kill are downgraded to incomplete records here, before
	// anything can present them as results.
	if purged, err := db.PurgeStagingScans(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("clean staging snapshots: %w", err)
	} else if purged > 0 {
		options.OnStartupCleanup(purged)
	}
	cfg := config.Default()
	roots, err := db.AuthorizedRoots(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	cfg.Roots = validatePersistedRoots(roots)
	latest, err := db.LatestScan(context.Background())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, fmt.Errorf("restore latest scan: %w", err)
	}
	factory := options.ScanFactory
	if factory == nil {
		sink := newScanSink(db, options.DataDir)
		factory = func(cfg config.Config) (ScanEngine, error) {
			executable, err := scanbroker.ResolveScannerExecutable()
			if err != nil {
				return nil, err
			}
			return scanbroker.New(scanbroker.Options{Config: cfg, Executable: executable, Sink: sink}), nil
		}
	}
	svc := &Service{db: db, cfg: cfg, manager: newScanManager(options.Context, db, latest, factory), recycle: newRecycleManager()}
	svc.agent = newAgentManager(db, options.Model, options.Secrets, filepath.Join(options.DataDir, "secrets"), svc.agentSnapshot)
	return svc, nil
}

func (s *Service) Close(ctx context.Context) error {
	s.agent.CancelAll()
	if err := s.manager.Close(ctx); err != nil {
		return err
	}
	return s.db.Close()
}

func (s *Service) Roots(context.Context) RootsDTO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roots := append([]string(nil), s.cfg.Roots...)
	return rootsDTO(roots)
}

func (s *Service) AddRoot(ctx context.Context, path string) (RootsDTO, error) {
	root, err := pathsafe.ValidateRoot(path)
	if err != nil {
		return RootsDTO{}, err
	}
	if err := pathsafe.RejectSymlinkComponents(root); err != nil {
		return RootsDTO{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanRunning() {
		return RootsDTO{}, ErrScanRunning
	}
	roots := append([]string(nil), s.cfg.Roots...)
	for _, current := range roots {
		if strings.EqualFold(current, root) {
			return rootsDTO(roots), nil
		}
	}
	roots = append(roots, root)
	sort.Strings(roots)
	if err := s.db.SaveAuthorizedRoots(ctx, roots); err != nil {
		return RootsDTO{}, err
	}
	s.cfg.Roots = roots
	return rootsDTO(roots), nil
}

// AuthorizeRoots validates an explicitly confirmed batch before changing the
// allowlist. A validation failure leaves both the persisted and in-memory
// allowlists unchanged. Valid roots are added to the existing allowlist; a
// parent replaces descendants because it grants the same read-only coverage.
func (s *Service) AuthorizeRoots(ctx context.Context, request BatchRootsRequestDTO) (BatchRootsDTO, error) {
	result := BatchRootsDTO{Results: make([]RootAuthorizationResultDTO, len(request.Paths))}
	canonical := make([]string, len(request.Paths))
	valid := len(request.Paths) > 0
	if len(request.Paths) == 0 {
		result.Results = []RootAuthorizationResultDTO{{Status: RootAuthorizationInvalid, Error: "at least one root path is required"}}
	}
	for i, requested := range request.Paths {
		item := RootAuthorizationResultDTO{RequestedPath: requested}
		root, err := pathsafe.ValidateRoot(requested)
		if err != nil {
			item.Status = RootAuthorizationInvalid
			item.Error = err.Error()
			valid = false
		} else {
			canonical[i] = root
			item.CanonicalPath = root
			item.Status = RootAuthorizationValidated
		}
		result.Results[i] = item
	}

	s.mu.Lock()
	if !valid {
		result.Roots = rootsDTO(s.cfg.Roots).Roots
		s.mu.Unlock()
		return result, nil
	}
	if s.scanRunning() {
		result.Roots = rootsDTO(s.cfg.Roots).Roots
		result.ScanError = ErrScanRunning.Error()
		s.mu.Unlock()
		return result, nil
	}

	previous := append([]string(nil), s.cfg.Roots...)
	intended := collapseRoots(append(previous, canonical...))
	if err := s.db.SaveAuthorizedRoots(ctx, intended); err != nil {
		s.mu.Unlock()
		return BatchRootsDTO{}, err
	}
	s.cfg.Roots = intended
	result.AuthorizationSucceeded = true
	result.Roots = rootsDTO(intended).Roots
	for i, root := range canonical {
		switch {
		case firstEqualFold(canonical, root) != i:
			result.Results[i].Status = RootAuthorizationDuplicate
		case containsEqualFold(previous, root):
			result.Results[i].Status = RootAuthorizationAlreadyAuthorized
		case containsEqualFold(intended, root):
			result.Results[i].Status = RootAuthorizationAuthorized
		default:
			result.Results[i].Status = RootAuthorizationCovered
		}
	}
	s.mu.Unlock()

	if request.StartScan {
		scan, err := s.StartScan(ctx)
		if err != nil {
			result.ScanError = err.Error()
		} else {
			result.Scan = &scan
		}
	}
	return result, nil
}

func (s *Service) RemoveRoot(ctx context.Context, path string) (RootsDTO, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanRunning() {
		return RootsDTO{}, ErrScanRunning
	}
	roots := make([]string, 0, len(s.cfg.Roots))
	for _, root := range s.cfg.Roots {
		if !strings.EqualFold(root, path) {
			roots = append(roots, root)
		}
	}
	if err := s.db.SaveAuthorizedRoots(ctx, roots); err != nil {
		return RootsDTO{}, err
	}
	s.cfg.Roots = roots
	return rootsDTO(roots), nil
}

func (s *Service) StartScan(context.Context) (StartScanDTO, error) {
	s.mu.RLock()
	cfg := s.cfg
	cfg.Roots = append([]string(nil), s.cfg.Roots...)
	s.mu.RUnlock()
	return s.manager.Start(cfg)
}

func (s *Service) CancelScan(context.Context) error { return s.manager.Cancel() }

// PauseScan suspends the running scan; the scanner process stays alive.
func (s *Service) PauseScan(context.Context) error { return s.manager.Pause() }

// ResumeScan continues a paused scan.
func (s *Service) ResumeScan(context.Context) error { return s.manager.Resume() }

func (s *Service) scanRunning() bool {
	_, state, _, _, _, _, _ := s.manager.snapshot()
	return state != ScanIdle
}

func (s *Service) Status(context.Context) StatusDTO {
	scan, state, taskID, taskResult, lastErr, progress, hasProgress := s.manager.snapshot()
	lastScan := ""
	if !scan.EndedAt.IsZero() {
		lastScan = scan.EndedAt.Local().Format("2006-01-02 15:04")
	}
	network, _ := s.db.NetworkEnabled(context.Background(), defaultProfileID)
	status := StatusDTO{Scanning: state != ScanIdle, State: state, ScanID: taskID, TaskResult: taskResult, LastScan: lastScan, LastError: lastErr, Stats: stats(scan), ScanReadOnly: true, Network: network, Partial: scan.Partial, Truncated: scan.Truncated, TruncationCause: scan.TruncationReason, ErrorCount: scan.ErrorCount}
	if hasProgress && state != ScanIdle {
		status.Progress = &ScanProgressDTO{
			Phase: string(progress.Phase), ObservedEntries: progress.ObservedEntries, Files: progress.Files,
			Directories: progress.Directories, Bytes: progress.Bytes, RootsStarted: progress.RootsStarted,
			RootsCompleted: progress.RootsCompleted, RootsTotal: progress.RootsTotal,
			CurrentRootIndex: progress.CurrentRootIndex, CurrentRootLabel: progress.CurrentRootLabel,
			ElapsedMS:      progress.Elapsed.Milliseconds(),
			EntryBudget:    ProgressBudgetDTO{Limit: int64(progress.EntryBudget), Used: progress.ObservedEntries},
			ErrorBudget:    ProgressBudgetDTO{Limit: int64(progress.ErrorBudget), Used: int64(progress.Errors)},
			DurationBudget: ProgressBudgetDTO{Limit: progress.DurationBudget.Milliseconds(), Used: progress.Elapsed.Milliseconds()},
			Cancelling:     progress.Cancelling || state == ScanCancelling, BudgetTruncated: progress.BudgetTruncated,
			Paused:         progress.Paused || state == ScanPaused,
			TruncationReason: progress.TruncationReason,
		}
	}
	return status
}

func (s *Service) Map(ctx context.Context) MapDTO {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	ignored, _ := s.db.IgnoredRecommendations(ctx, scan.ID)
	total := len(mapEntries(scan))
	out := MapDTO{Nodes: buildNodes(scan, false), Stats: stats(scan), Recommendations: recommendationsFiltered(scan, ignored), NodesTotal: total}
	if total > len(out.Nodes) {
		out.NodesTruncated = true
		out.NodesOmitted = total - len(out.Nodes)
	}
	return out
}

func (s *Service) Node(_ context.Context, id string) (NodeDTO, error) {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	for _, entry := range scan.Entries {
		if entry.ID == id {
			return nodeFromEntry(entry, true, 0), nil
		}
	}
	return NodeDTO{}, ErrNodeNotFound
}

func (s *Service) Reveal(ctx context.Context, id string) (RevealDTO, error) {
	node, err := s.Node(ctx, id)
	if err != nil {
		return RevealDTO{}, err
	}
	if !s.authorized(node.Path) {
		return RevealDTO{}, ErrUnauthorized
	}
	if err := platform.Reveal(node.Path); err != nil {
		return RevealDTO{}, err
	}
	return RevealDTO{OK: true}, nil
}

func (s *Service) Privacy(ctx context.Context) PrivacyDTO {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	recycled, _ := s.db.RecycledItems(ctx, 500)
	s.mu.RLock()
	defer s.mu.RUnlock()
	capabilities := CapabilitiesDTO{
		RecycleBin:          "仅逐项确认后",
		LocalHash:           s.cfg.HashSHA256,
		AuthorizedRootCount: len(s.cfg.Roots),
		RecycledItemCount:   len(recycled),
	}
	return PrivacyDTO{Capabilities: capabilities, AgentPayload: privacy.Isolate(scan), ExcludedNames: append([]string(nil), s.cfg.ExcludedNames...)}
}

func (s *Service) Demo(context.Context) DemoDTO { return DemoDTO{Nodes: demoNodes()} }

func (s *Service) IgnoreRecommendation(ctx context.Context, id string) error {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	if scan.ID == "" {
		return ErrNodeNotFound
	}
	for _, recommendation := range recommendations(scan) {
		if recommendation.ID == id {
			return s.db.IgnoreRecommendation(ctx, scan.ID, id)
		}
	}
	return ErrNodeNotFound
}

func (s *Service) agentSnapshot() model.Scan {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	return scan
}

func (s *Service) authorized(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, root := range s.cfg.Roots {
		if _, err := pathsafe.Contained(root, path); err == nil {
			return true
		}
	}
	return false
}

func validatePersistedRoots(roots []string) []string {
	var out []string
	for _, root := range roots {
		clean, err := pathsafe.ValidateRoot(root)
		if err == nil {
			out = append(out, clean)
		}
	}
	sort.Strings(out)
	return out
}

func rootsDTO(roots []string) RootsDTO {
	out := RootsDTO{Roots: make([]RootDTO, 0, len(roots))}
	for _, root := range roots {
		out.Roots = append(out.Roots, RootDTO{Path: root})
	}
	return out
}

func collapseRoots(roots []string) []string {
	sort.SliceStable(roots, func(i, j int) bool {
		if len(roots[i]) != len(roots[j]) {
			return len(roots[i]) < len(roots[j])
		}
		return strings.ToLower(roots[i]) < strings.ToLower(roots[j])
	})
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		covered := false
		for _, parent := range out {
			if strings.EqualFold(parent, root) {
				covered = true
				break
			}
			if _, err := pathsafe.Contained(parent, root); err == nil {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, root)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func containsEqualFold(paths []string, target string) bool {
	return firstEqualFold(paths, target) >= 0
}

func firstEqualFold(paths []string, target string) int {
	for i, path := range paths {
		if strings.EqualFold(path, target) {
			return i
		}
	}
	return -1
}

// mapNodeLimit caps how many entries the map renders. Stats are computed over
// the whole scan, so the map is a top-N view, not the full picture; MapDTO
// reports the omission rather than letting the canvas imply completeness.
const mapNodeLimit = 180

func buildNodes(scan model.Scan, paths bool) []NodeDTO {
	entries := mapEntries(scan)
	if len(entries) > mapNodeLimit {
		entries = entries[:mapNodeLimit]
	}
	nodes := make([]NodeDTO, 0, len(entries))
	for i, entry := range entries {
		nodes = append(nodes, nodeFromEntry(entry, paths, i))
	}
	return nodes
}

func mapEntries(scan model.Scan) []model.Entry {
	entries := make([]model.Entry, 0, len(scan.Entries))
	for _, entry := range scan.Entries {
		if entry.Kind == model.KindFile || entry.GitProject {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Size > entries[j].Size })
	return entries
}

func nodeFromEntry(entry model.Entry, paths bool, index int) NodeDTO {
	zone, health, insight := zoneFor(entry)
	angle := float64(index) * 2.399963
	distance := 80 + float64(index/5)*28
	node := NodeDTO{ID: entry.ID, Name: entry.Name, Size: entry.Size, Zone: zone, Health: health, Modified: entry.ModTime.Format("2006-01-02"), Kind: string(entry.Kind), Insight: insight, X: distance * cos(angle), Y: distance * sin(angle)}
	if paths {
		node.Path = entry.Path
	}
	return node
}

// zonePriority fixes the order in which competing classes decide a node's zone.
//
// classify.Apply sorts Entry.Classes alphabetically, so ranging over that slice
// resolved ties by spelling rather than by meaning: "dormant" sorts before
// "giant", "orphan" and "rotten", so a 50 GB file untouched for a year landed
// in 僵尸墓地 instead of 巨物火山. Worse, config.Validate guarantees
// RottenAge >= DormantAge, so "rotten" always co-occurs with "dormant" and the
// decay zone was unreachable entirely. Order here is intent, most specific
// first; "dormant" is the weakest signal and must be considered last.
var zonePriority = []model.Class{
	model.ClassGitZombie,
	model.ClassRotten,
	model.ClassGiant,
	model.ClassOrphan,
	model.ClassSeedling,
	model.ClassDormant,
}

func zoneFor(entry model.Entry) (string, int, string) {
	has := func(want model.Class) bool {
		for _, class := range entry.Classes {
			if class == want {
				return true
			}
		}
		return false
	}
	for _, class := range zonePriority {
		if !has(class) {
			continue
		}
		switch class {
		case model.ClassGitZombie:
			return "zombies", 25, "项目长期停滞；请在资源管理器中自行检查未完成工作。"
		case model.ClassRotten:
			return "decay", 30, "它符合长期未变化的临时文件特征，系统不会替你处理。"
		case model.ClassGiant:
			return "giants", 45, "它占用了显著空间，仅供你了解。"
		case model.ClassOrphan:
			return "downloads", 38, "它具有孤儿文件特征，结论仅基于元数据。"
		case model.ClassSeedling:
			return "seedlings", 90, "这是近期出现的新文件。"
		case model.ClassDormant:
			return "zombies", 42, "它已经较长时间没有变化。"
		}
	}
	return "active", 82, "近期仍有变化，生态状态活跃。"
}

func recommendations(scan model.Scan) []RecommendationDTO { return recommendationsFiltered(scan, nil) }

func recommendationsFiltered(scan model.Scan, ignored map[string]bool) []RecommendationDTO {
	var out []RecommendationDTO
	for _, entry := range scan.Entries {
		if ignored[entry.ID] {
			continue
		}
		if len(entry.Classes) > 1 || (len(entry.Classes) == 1 && entry.Classes[0] != model.ClassActive) {
			out = append(out, RecommendationDTO{ID: entry.ID, NodeID: entry.ID})
		}
		if len(out) >= 30 {
			break
		}
	}
	return out
}

func demoNodes() []NodeDTO {
	return []NodeDTO{{ID: "demo-active", Name: "活跃森林", Size: 12 << 30, Zone: "active", Health: 88, Kind: "目录", Insight: "近期持续生长。", X: -180, Y: -80}, {ID: "demo-giant", Name: "巨物火山", Size: 48 << 30, Zone: "giants", Health: 44, Kind: "目录", Insight: "大型文件聚集地。", X: 180, Y: -60}, {ID: "demo-download", Name: "下载荒原", Size: 7 << 30, Zone: "downloads", Health: 39, Kind: "目录", Insight: "长期未变化的下载物。", X: -60, Y: 150}, {ID: "demo-zombie", Name: "僵尸墓地", Size: 4 << 30, Zone: "zombies", Health: 25, Kind: "项目", Insight: "停滞项目。", X: 230, Y: 170}}
}

func cos(x float64) float64 {
	const pi = 3.141592653589793
	x -= float64(int(x/(2*pi))) * (2 * pi)
	term, sum := 1.0, 1.0
	for n := 1; n < 9; n++ {
		term *= -x * x / float64((2*n-1)*(2*n))
		sum += term
	}
	return sum
}
func sin(x float64) float64 { return cos(1.5707963267948966 - x) }
