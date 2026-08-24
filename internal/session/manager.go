package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type managedSession struct {
	record    Record
	persistMu sync.Mutex
}

type Manager struct {
	config       Config
	persistence  *persistence
	git          Git
	worktreeRoot string

	mu       sync.RWMutex
	sessions map[string]*managedSession
}

var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func NewManager(ctx context.Context, config Config) (*Manager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.IDFactory == nil {
		config.IDFactory = randomUUID
	}
	if config.Git == nil {
		config.Git = execGit{}
	}
	store, err := newPersistence(config.StorageRoot)
	if err != nil {
		return nil, err
	}
	worktreeRoot, err := canonicalManagedRoot(config.WorktreeRoot)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		config: config, persistence: store, git: config.Git, worktreeRoot: worktreeRoot,
		sessions: map[string]*managedSession{},
	}
	if err := manager.Recover(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func randomUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], value[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], value[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], value[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], value[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], value[10:16])
	return string(buf)
}

func (m *Manager) Open(ctx context.Context, request OpenRequest) (Record, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOpenRequest(request); err != nil {
		return Record{}, err
	}
	inputRoot, err := canonicalExistingDir(request.RepoRoot)
	if err != nil {
		return Record{}, err
	}
	repoRoot, isGit, err := m.git.DetectRepository(ctx, inputRoot)
	if err != nil {
		return Record{}, newError(CodeInvalidRepoRoot, "detect Git repository", err)
	}
	if isGit {
		repoRoot, err = canonicalExistingDir(repoRoot)
		if err != nil {
			return Record{}, err
		}
	} else {
		repoRoot = inputRoot
	}
	isolation, err := normalizeIsolation(isGit, request.IsolationMode)
	if err != nil {
		return Record{}, err
	}
	permission := strings.ToLower(strings.TrimSpace(request.PermissionMode))
	if permission == "" {
		permission = "project"
	}

	var baseRef *string
	commit := ""
	if isGit {
		resolvedRef, resolvedCommit, resolveErr := m.git.ResolveBaseRef(ctx, repoRoot, strings.TrimSpace(request.BaseRef))
		if resolveErr != nil {
			if ErrorCode(resolveErr) == CodeInvalidBaseRef {
				return Record{}, resolveErr
			}
			return Record{}, newError(CodeInvalidBaseRef, "resolve base_ref", resolveErr)
		}
		baseRef = stringPtr(resolvedRef)
		commit = resolvedCommit
	} else if strings.TrimSpace(request.BaseRef) != "" {
		return Record{}, newError(CodeInvalidBaseRef, "base_ref is only valid for Git sessions", nil)
	}

	sessionID, err := m.allocateSessionID()
	if err != nil {
		return Record{}, err
	}
	now := m.config.Now().UTC()
	record := Record{
		SessionID: sessionID, ProjectID: strings.TrimSpace(request.ProjectID), RepoRoot: repoRoot,
		WorkspaceType: WorkspaceNonGit, IsolationMode: isolation, BaseRef: baseRef,
		PermissionMode: permission, RuntimeID: nil, JobIDs: []string{}, ClientTag: optionalString(request.ClientTag),
		CreatedAt: now, LastActiveAt: now, State: StateOpening, FailureReason: nil, MetadataVersion: ContractVersion,
	}
	if isGit {
		branch := branchName(sessionID)
		worktree, worktreeErr := managedWorktreePath(m.worktreeRoot, sessionID)
		if worktreeErr != nil {
			return Record{}, worktreeErr
		}
		record.WorkspaceType = WorkspaceGit
		record.Branch = stringPtr(branch)
		record.Worktree = stringPtr(worktree)
	}

	managed := &managedSession{record: record}
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return Record{}, newError(CodeConflict, "session_id collision", nil)
	}
	if record.WorkspaceType == WorkspaceNonGit {
		if err := m.nonGitConflictLocked(record); err != nil {
			m.mu.Unlock()
			return Record{}, err
		}
	}
	m.sessions[sessionID] = managed
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		return Record{}, err
	}

	if isGit {
		if err := m.git.AddWorktree(ctx, repoRoot, *record.Worktree, *record.Branch, commit); err != nil {
			return m.failOpening(managed, err)
		}
		canonicalWorktree, err := canonicalExistingDir(*record.Worktree)
		if err != nil || !pathWithin(m.worktreeRoot, canonicalWorktree) || pathEqual(m.worktreeRoot, canonicalWorktree) {
			if err == nil {
				err = errors.New("worktree escaped managed root")
			}
			return m.failOpening(managed, newError(CodeInvalidRepoRoot, "verify managed worktree", err))
		}
		branch, err := m.git.CurrentBranch(ctx, canonicalWorktree)
		if err != nil || strings.TrimSpace(branch) != *record.Branch {
			if err == nil {
				err = fmt.Errorf("expected branch %q, got %q", *record.Branch, branch)
			}
			return m.failOpening(managed, newError(CodeConflict, "verify managed worktree branch", err))
		}
		m.mu.Lock()
		managed.record.Worktree = stringPtr(canonicalWorktree)
		m.mu.Unlock()
	}

	m.mu.Lock()
	if err := transition(&managed.record, StateActive); err != nil {
		m.mu.Unlock()
		return m.failOpening(managed, err)
	}
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func validateOpenRequest(request OpenRequest) error {
	project := strings.TrimSpace(request.ProjectID)
	if len(project) < 1 || len(project) > 128 || !projectIDPattern.MatchString(project) {
		return newError(CodeInvalidRepoRoot, "project_id does not match D1-v1 contract", nil)
	}
	permission := strings.ToLower(strings.TrimSpace(request.PermissionMode))
	if permission != "" && permission != "project" && permission != "full" {
		return newError(CodeInvalidRepoRoot, "permission_mode must be project or full", nil)
	}
	if len(strings.TrimSpace(request.ClientTag)) > 256 {
		return newError(CodeInvalidRepoRoot, "client_tag exceeds D1-v1 maximum", nil)
	}
	return nil
}

func normalizeIsolation(isGit bool, raw IsolationMode) (IsolationMode, error) {
	mode := IsolationMode(strings.ToLower(strings.TrimSpace(string(raw))))
	if isGit {
		if mode == "" || mode == IsolationWorktree {
			return IsolationWorktree, nil
		}
		return "", newError(CodeConflict, "Git sessions require worktree isolation", nil)
	}
	if mode == "" {
		return IsolationExclusive, nil
	}
	switch mode {
	case IsolationExclusive, IsolationSharedReadonly, IsolationSharedExplicit:
		return mode, nil
	default:
		return "", newError(CodeConflict, "invalid non-Git isolation_mode", nil)
	}
}

func (m *Manager) allocateSessionID() (string, error) {
	for attempt := 0; attempt < 32; attempt++ {
		id := strings.TrimSpace(m.config.IDFactory())
		if !validOpaqueID(id) {
			return "", newError(CodeConflict, "generated session_id is invalid", nil)
		}
		m.mu.RLock()
		_, exists := m.sessions[id]
		m.mu.RUnlock()
		if !exists {
			return id, nil
		}
	}
	return "", newError(CodeConflict, "unable to allocate unique session_id", nil)
}

func validOpaqueID(id string) bool {
	if len(id) < 1 || len(id) > 128 || strings.ContainsRune(id, 0) || strings.ContainsAny(id, `/\\:`) || id == "." || id == ".." || strings.Contains(id, "..") {
		return false
	}
	return true
}

func branchName(sessionID string) string {
	value := strings.ToLower(strings.ReplaceAll(sessionID, "-", ""))
	value = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-")
	if len(value) > 48 {
		value = value[:48]
	}
	if value == "" {
		value = "session"
	}
	return "chatgpt/session/" + value
}

func (m *Manager) failOpening(managed *managedSession, failure error) (Record, error) {
	m.mu.Lock()
	if managed.record.State == StateOpening {
		_ = transition(&managed.record, StateFailed)
	}
	reason := truncateFailure(failure)
	managed.record.FailureReason = stringPtr(reason)
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	_ = m.persist(managed)
	if ErrorCode(failure) != "" {
		return result, failure
	}
	return result, newError(CodeConflict, "session opening failed", failure)
}

func (m *Manager) Get(sessionID string) (Record, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Record{}, newError(CodeNotFound, "session not found", nil)
	}
	m.mu.RLock()
	managed := m.sessions[sessionID]
	if managed == nil {
		m.mu.RUnlock()
		return Record{}, newError(CodeNotFound, "session not found", nil)
	}
	result := cloneRecord(managed.record)
	m.mu.RUnlock()
	return result, nil
}

func (m *Manager) List(filter ListFilter) []Record {
	projectID := strings.TrimSpace(filter.ProjectID)
	clientTag := strings.TrimSpace(filter.ClientTag)
	m.mu.RLock()
	items := make([]Record, 0, len(m.sessions))
	for _, managed := range m.sessions {
		record := managed.record
		if filter.ActiveOnly && !activeState(record.State) {
			continue
		}
		if projectID != "" && record.ProjectID != projectID {
			continue
		}
		if clientTag != "" && (record.ClientTag == nil || *record.ClientTag != clientTag) {
			continue
		}
		items = append(items, cloneRecord(record))
	}
	m.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items
}

func (m *Manager) Resolve(sessionID, requestedWorkspace string) (Record, error) {
	record, err := m.Get(sessionID)
	if err != nil {
		return Record{}, err
	}
	switch record.State {
	case StateClosed:
		return Record{}, newError(CodeClosed, "session is closed", nil)
	case StateFailed:
		return Record{}, newError(CodeFailed, "session is failed", nil)
	case StateActive:
	default:
		return Record{}, newError(CodeInvalidTransition, "session is not ACTIVE", nil)
	}
	requested := strings.TrimSpace(requestedWorkspace)
	if requested == "" {
		return record, nil
	}
	canonical, err := canonicalExistingDir(requested)
	if err != nil || !pathEqual(canonical, record.Workspace()) {
		return Record{}, newError(CodeWorkspaceMismatch, "workspace does not match session canonical workspace", err)
	}
	return record, nil
}

func (m *Manager) Touch(sessionID string) (Record, error) {
	managed, err := m.activeManaged(sessionID)
	if err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func (m *Manager) SetRuntime(sessionID, runtimeID string) (Record, error) {
	managed, err := m.activeManaged(sessionID)
	if err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	managed.record.RuntimeID = optionalString(runtimeID)
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func (m *Manager) AttachJob(sessionID, jobID string) (Record, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return Record{}, errors.New("job_id is required")
	}
	managed, err := m.activeManaged(sessionID)
	if err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	for _, existing := range managed.record.JobIDs {
		if existing == jobID {
			result := cloneRecord(managed.record)
			m.mu.Unlock()
			return result, nil
		}
	}
	managed.record.JobIDs = append(managed.record.JobIDs, jobID)
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func (m *Manager) Close(_ context.Context, sessionID string) (Record, error) {
	m.mu.Lock()
	managed := m.sessions[strings.TrimSpace(sessionID)]
	if managed == nil {
		m.mu.Unlock()
		return Record{}, newError(CodeNotFound, "session not found", nil)
	}
	if managed.record.State == StateClosed {
		result := cloneRecord(managed.record)
		m.mu.Unlock()
		return result, nil
	}
	if managed.record.State == StateFailed {
		result := cloneRecord(managed.record)
		m.mu.Unlock()
		return result, newError(CodeFailed, "failed session cannot be closed in D1", nil)
	}
	if managed.record.State != StateActive {
		m.mu.Unlock()
		return Record{}, newError(CodeInvalidTransition, "session cannot close from current state", nil)
	}
	if err := transition(&managed.record, StateClosing); err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	managed.record.LastActiveAt = m.config.Now().UTC()
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	if err := transition(&managed.record, StateClosed); err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	managed.record.RuntimeID = nil
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func (m *Manager) Fail(sessionID, reason string) (Record, error) {
	m.mu.Lock()
	managed := m.sessions[strings.TrimSpace(sessionID)]
	if managed == nil {
		m.mu.Unlock()
		return Record{}, newError(CodeNotFound, "session not found", nil)
	}
	if managed.record.State == StateClosed || managed.record.State == StateFailed {
		result := cloneRecord(managed.record)
		m.mu.Unlock()
		return result, newError(CodeInvalidTransition, "terminal session cannot transition to FAILED", nil)
	}
	if err := transition(&managed.record, StateFailed); err != nil {
		m.mu.Unlock()
		return Record{}, err
	}
	failure := strings.TrimSpace(reason)
	if failure == "" {
		failure = "session failed"
	}
	if len(failure) > 4096 {
		failure = failure[:4096]
	}
	managed.record.FailureReason = stringPtr(failure)
	managed.record.RuntimeID = nil
	managed.record.LastActiveAt = m.config.Now().UTC()
	result := cloneRecord(managed.record)
	m.mu.Unlock()
	if err := m.persist(managed); err != nil {
		return Record{}, err
	}
	return result, nil
}

func (m *Manager) activeManaged(sessionID string) (*managedSession, error) {
	m.mu.RLock()
	managed := m.sessions[strings.TrimSpace(sessionID)]
	if managed == nil {
		m.mu.RUnlock()
		return nil, newError(CodeNotFound, "session not found", nil)
	}
	state := managed.record.State
	m.mu.RUnlock()
	switch state {
	case StateClosed:
		return nil, newError(CodeClosed, "session is closed", nil)
	case StateFailed:
		return nil, newError(CodeFailed, "session is failed", nil)
	case StateActive:
		return managed, nil
	default:
		return nil, newError(CodeInvalidTransition, "session is not ACTIVE", nil)
	}
}

func (m *Manager) nonGitConflictLocked(candidate Record) error {
	for _, managed := range m.sessions {
		existing := managed.record
		if existing.WorkspaceType != WorkspaceNonGit || !activeState(existing.State) || !pathEqual(existing.RepoRoot, candidate.RepoRoot) {
			continue
		}
		if nonGitModesConflict(existing.IsolationMode, candidate.IsolationMode) {
			return newError(CodeConflict, "canonical non-Git workspace already has an incompatible active session", nil)
		}
	}
	return nil
}

func nonGitModesConflict(a, b IsolationMode) bool {
	if a == IsolationExclusive || b == IsolationExclusive {
		return true
	}
	return a != b
}

func (m *Manager) Recover(ctx context.Context) error {
	records, err := m.persistence.loadAll()
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].SessionID < records[j].SessionID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	for _, record := range records {
		m.sessions[record.SessionID] = &managedSession{record: cloneRecord(record)}
	}
	for _, record := range records {
		managed := m.sessions[record.SessionID]
		changed := false
		switch managed.record.State {
		case StateOpening:
			_ = transition(&managed.record, StateFailed)
			managed.record.FailureReason = stringPtr("AgentDock restarted while session was OPENING")
			changed = true
		case StateClosing:
			_ = transition(&managed.record, StateFailed)
			managed.record.FailureReason = stringPtr("AgentDock restarted while session was CLOSING")
			changed = true
		case StateActive:
			if err := m.validateRecoveredWorkspace(ctx, managed.record); err != nil {
				_ = transition(&managed.record, StateFailed)
				managed.record.FailureReason = stringPtr(truncateFailure(fmt.Errorf("restart recovery: %w", err)))
			}
			managed.record.RuntimeID = nil
			changed = true
		}
		if changed {
			managed.record.LastActiveAt = m.config.Now().UTC()
			if err := m.persist(managed); err != nil {
				return err
			}
		}
	}
	return m.recoverNonGitConflicts()
}

func (m *Manager) validateRecoveredWorkspace(ctx context.Context, record Record) error {
	root, err := canonicalExistingDir(record.RepoRoot)
	if err != nil || !pathEqual(root, record.RepoRoot) {
		return errors.New("canonical repo_root unavailable or changed")
	}
	if record.WorkspaceType == WorkspaceNonGit {
		return nil
	}
	if record.WorkspaceType != WorkspaceGit || record.Worktree == nil || record.Branch == nil {
		return errors.New("invalid Git session metadata")
	}
	worktree, err := canonicalExistingDir(*record.Worktree)
	if err != nil {
		return err
	}
	if !pathWithin(m.worktreeRoot, worktree) || pathEqual(m.worktreeRoot, worktree) {
		return errors.New("worktree escaped managed root")
	}
	branch, err := m.git.CurrentBranch(ctx, worktree)
	if err != nil || strings.TrimSpace(branch) != *record.Branch {
		return errors.New("worktree branch mismatch")
	}
	return nil
}

func (m *Manager) recoverNonGitConflicts() error {
	m.mu.Lock()
	items := make([]*managedSession, 0, len(m.sessions))
	for _, managed := range m.sessions {
		if managed.record.WorkspaceType == WorkspaceNonGit && activeState(managed.record.State) {
			items = append(items, managed)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].record.CreatedAt.Before(items[j].record.CreatedAt) })
	seen := map[string]*managedSession{}
	changed := make([]*managedSession, 0)
	for _, managed := range items {
		key := cleanKey(managed.record.RepoRoot)
		prior := seen[key]
		if prior == nil {
			seen[key] = managed
			continue
		}
		if !nonGitModesConflict(prior.record.IsolationMode, managed.record.IsolationMode) {
			continue
		}
		if err := transition(&managed.record, StateFailed); err == nil {
			managed.record.FailureReason = stringPtr("restart recovery detected conflicting non-Git session ownership")
			managed.record.LastActiveAt = m.config.Now().UTC()
			changed = append(changed, managed)
		}
	}
	m.mu.Unlock()
	for _, managed := range changed {
		if err := m.persist(managed); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) persist(managed *managedSession) error {
	managed.persistMu.Lock()
	defer managed.persistMu.Unlock()
	m.mu.RLock()
	snapshot := cloneRecord(managed.record)
	m.mu.RUnlock()
	return m.persistence.save(snapshot)
}

func cloneRecord(record Record) Record {
	copy := record
	copy.BaseRef = cloneString(record.BaseRef)
	copy.Branch = cloneString(record.Branch)
	copy.Worktree = cloneString(record.Worktree)
	copy.RuntimeID = cloneString(record.RuntimeID)
	copy.ClientTag = cloneString(record.ClientTag)
	copy.FailureReason = cloneString(record.FailureReason)
	copy.JobIDs = append([]string(nil), record.JobIDs...)
	if copy.JobIDs == nil {
		copy.JobIDs = []string{}
	}
	return copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func stringPtr(value string) *string { copy := value; return &copy }
func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return stringPtr(value)
}
func truncateFailure(err error) string {
	if err == nil {
		return "session failed"
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 4096 {
		value = value[:4096]
	}
	if value == "" {
		value = "session failed"
	}
	return value
}
