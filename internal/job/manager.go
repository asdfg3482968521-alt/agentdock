package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type managedJob struct {
	record           Record
	arguments        map[string]any
	output           *outputStore
	workspaceKey     string
	progress         map[string]any
	slotReserved     bool
	timeoutTriggered bool
	timer            *time.Timer
	cancelStarted    bool
	cancelDone       chan struct{}
	cancelErr        error
	persistMu        sync.Mutex
}

type Manager struct {
	executor    RuntimeExecutor
	config      Config
	persistence *persistence

	mu                sync.RWMutex
	jobs              map[string]*managedJob
	queue             []string
	active            int
	activeByWorkspace map[string]int
	writeByWorkspace  map[string]int
}

func NewManager(_ context.Context, executor RuntimeExecutor, config Config) (*Manager, error) {
	if executor == nil {
		return nil, errors.New("job runtime executor is required")
	}
	config = normalizeConfig(config)
	if err := validateLimits(config.Limits); err != nil {
		return nil, err
	}
	store, err := newPersistence(config.StorageRoot)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		executor: executor, config: config, persistence: store,
		jobs: map[string]*managedJob{}, activeByWorkspace: map[string]int{}, writeByWorkspace: map[string]int{},
	}
	if err := manager.recover(); err != nil {
		return nil, err
	}
	if err := manager.cleanupRetention(); err != nil {
		return nil, err
	}
	return manager, nil
}

func normalizeConfig(config Config) Config {
	defaults := DefaultLimits()
	l := config.Limits
	if l.MaxConcurrentJobs == 0 {
		l.MaxConcurrentJobs = defaults.MaxConcurrentJobs
	}
	if l.MaxConcurrentPerWorkspace == 0 {
		l.MaxConcurrentPerWorkspace = defaults.MaxConcurrentPerWorkspace
	}
	if l.MaxConcurrentWritePerWorkspace == 0 {
		l.MaxConcurrentWritePerWorkspace = defaults.MaxConcurrentWritePerWorkspace
	}
	if l.MemoryOutputBytesPerJob == 0 {
		l.MemoryOutputBytesPerJob = defaults.MemoryOutputBytesPerJob
	}
	if l.MaxOutputBytesPerResponse == 0 {
		l.MaxOutputBytesPerResponse = defaults.MaxOutputBytesPerResponse
	}
	if l.MaxDiskOutputBytesPerJob == 0 {
		l.MaxDiskOutputBytesPerJob = defaults.MaxDiskOutputBytesPerJob
	}
	if l.Retention == 0 {
		l.Retention = defaults.Retention
	}
	if l.RetentionMaxCompletedJobs == 0 {
		l.RetentionMaxCompletedJobs = defaults.RetentionMaxCompletedJobs
	}
	if l.MaxArgumentSummaryBytes == 0 {
		l.MaxArgumentSummaryBytes = defaults.MaxArgumentSummaryBytes
	}
	if l.MaxMetadataSummaryBytes == 0 {
		l.MaxMetadataSummaryBytes = defaults.MaxMetadataSummaryBytes
	}
	if l.MaxResultSummaryBytes == 0 {
		l.MaxResultSummaryBytes = defaults.MaxResultSummaryBytes
	}
	if l.MaxTimeout == 0 {
		l.MaxTimeout = defaults.MaxTimeout
	}
	config.Limits = l
	if config.RedactText == nil {
		config.RedactText = defaultRedactText
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.IDFactory == nil {
		config.IDFactory = randomUUID
	}
	return config
}

func validateLimits(l Limits) error {
	if l.MaxConcurrentJobs <= 0 || l.MaxConcurrentPerWorkspace <= 0 || l.MaxConcurrentWritePerWorkspace <= 0 ||
		l.MemoryOutputBytesPerJob <= 0 || l.MaxOutputBytesPerResponse <= 0 || l.MaxDiskOutputBytesPerJob <= 0 ||
		l.Retention <= 0 || l.RetentionMaxCompletedJobs <= 0 || l.MaxArgumentSummaryBytes <= 0 ||
		l.MaxMetadataSummaryBytes <= 0 || l.MaxResultSummaryBytes <= 0 || l.MaxTimeout <= 0 {
		return errors.New("job manager limits must be positive")
	}
	if l.MaxConcurrentWritePerWorkspace > l.MaxConcurrentPerWorkspace {
		return errors.New("job write concurrency cannot exceed per-workspace concurrency")
	}
	return nil
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

func (m *Manager) Start(request StartRequest) (StartResult, error) {
	if checker, ok := m.executor.(AvailabilityChecker); ok {
		if err := checker.Available(); err != nil {
			return StartResult{}, err
		}
	}
	if err := m.validateStart(request); err != nil {
		return StartResult{}, err
	}
	jobID := m.config.IDFactory()
	if err := validateJobID(jobID); err != nil {
		return StartResult{}, err
	}
	created := m.config.Now()
	class := request.ConcurrencyClass
	if class == "" {
		class = ConcurrencyWrite
	}
	mode := strings.TrimSpace(request.PermissionMode)
	if mode == "" {
		mode = "project"
	}
	args := cloneMap(request.Arguments)
	record := Record{
		JobID: jobID, Workspace: strings.TrimSpace(request.Workspace), Tool: strings.TrimSpace(request.Tool), PermissionMode: mode,
		ArgumentsSummary: summarize(request.Arguments, m.config.Limits.MaxArgumentSummaryBytes, m.config.RedactText),
		CreatedAt:        created, State: StateQueued, ConcurrencyClass: class,
	}
	if request.Metadata != nil {
		record.MetadataSummary = summarize(request.Metadata, m.config.Limits.MaxMetadataSummaryBytes, m.config.RedactText)
	}
	if request.Timeout > 0 {
		record.TimeoutMillis = request.Timeout.Milliseconds()
	}
	jobDir, _ := m.persistence.jobDir(jobID)
	output := newOutputStore(jobDir, m.config.Limits, m.config.RedactText, m.config.Now)
	if err := output.initialize(0, 0); err != nil {
		return StartResult{}, err
	}
	if err := output.append(RuntimeOutputEvent{Stream: "event", Text: "QUEUED\n"}); err != nil {
		return StartResult{}, err
	}
	record.OutputCursor, record.OutputBytes = output.cursorAndBytes()
	job := &managedJob{record: record, arguments: args, output: output, workspaceKey: workspaceKey(record.Workspace)}
	if err := m.persistence.save(record); err != nil {
		return StartResult{}, err
	}

	m.mu.Lock()
	if _, exists := m.jobs[jobID]; exists {
		m.mu.Unlock()
		return StartResult{}, errors.New("job_id collision")
	}
	m.jobs[jobID] = job
	m.queue = append(m.queue, jobID)
	m.mu.Unlock()
	go m.drain()
	go func() { _ = m.cleanupRetention() }()
	return StartResult{JobID: jobID, State: StateQueued, CreatedAt: created}, nil
}

func (m *Manager) validateStart(request StartRequest) error {
	if strings.TrimSpace(request.Workspace) == "" {
		return errors.New("workspace is required")
	}
	if strings.TrimSpace(request.Tool) == "" {
		return errors.New("tool is required")
	}
	mode := strings.TrimSpace(request.PermissionMode)
	if mode != "" && mode != "project" && mode != "full" {
		return errors.New("permission_mode must be project or full")
	}
	if request.Timeout < 0 || request.Timeout > m.config.Limits.MaxTimeout {
		return fmt.Errorf("timeout exceeds configured maximum of %s", m.config.Limits.MaxTimeout)
	}
	if request.ConcurrencyClass != "" && request.ConcurrencyClass != ConcurrencyRead && request.ConcurrencyClass != ConcurrencyWrite && request.ConcurrencyClass != ConcurrencyDestructive {
		return errors.New("invalid concurrency_class")
	}
	return nil
}

func (m *Manager) Status(jobID string) (StatusResult, error) {
	if err := validateJobID(jobID); err != nil {
		return StatusResult{}, err
	}
	m.mu.RLock()
	job := m.jobs[jobID]
	if job == nil {
		m.mu.RUnlock()
		return StatusResult{}, errors.New("job not found")
	}
	status := m.statusLocked(job)
	m.mu.RUnlock()
	return status, nil
}

func (m *Manager) List(filter ListFilter) []StatusResult {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	workspace := strings.TrimSpace(filter.Workspace)
	m.mu.RLock()
	items := make([]StatusResult, 0, len(m.jobs))
	for _, job := range m.jobs {
		if filter.State != "" && job.record.State != filter.State {
			continue
		}
		if workspace != "" && workspaceKey(job.record.Workspace) != workspaceKey(workspace) {
			continue
		}
		items = append(items, m.statusLocked(job))
	}
	m.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func (m *Manager) Output(request OutputRequest) (OutputResult, error) {
	if err := validateJobID(request.JobID); err != nil {
		return OutputResult{}, err
	}
	m.mu.RLock()
	job := m.jobs[request.JobID]
	m.mu.RUnlock()
	if job == nil {
		return OutputResult{}, errors.New("job not found")
	}
	result, err := job.output.read(request.Cursor, request.LimitBytes)
	if err != nil {
		return OutputResult{}, err
	}
	m.mu.RLock()
	result.Complete = job.record.State.Terminal() && result.NextCursor >= job.record.OutputCursor
	m.mu.RUnlock()
	return result, nil
}

func (m *Manager) Cancel(ctx context.Context, jobID, reason string) (StatusResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateJobID(jobID); err != nil {
		return StatusResult{}, err
	}
	m.mu.Lock()
	job := m.jobs[jobID]
	if job == nil {
		m.mu.Unlock()
		return StatusResult{}, errors.New("job not found")
	}
	if job.record.State.Terminal() {
		status := m.statusLocked(job)
		m.mu.Unlock()
		return status, nil
	}
	redactedReason := truncateUTF8(m.config.RedactText(reason), 1024)
	if redactedReason == "" {
		redactedReason = "user requested cancellation"
	}
	if job.record.State == StateQueued {
		m.removeQueuedLocked(jobID)
		job.record.CancelReason = redactedReason
		_ = transitionRecord(&job.record, StateCancelled)
		now := m.config.Now()
		job.record.FinishedAt = &now
		job.arguments = nil
		status := m.statusLocked(job)
		m.mu.Unlock()
		if err := m.persistJob(job); err != nil {
			return StatusResult{}, err
		}
		return status, nil
	}
	if job.record.State != StateCancelRequested {
		job.record.CancelReason = redactedReason
		if err := transitionRecord(&job.record, StateCancelRequested); err != nil {
			m.mu.Unlock()
			return StatusResult{}, err
		}
	}
	m.mu.Unlock()
	if err := m.persistJob(job); err != nil {
		return StatusResult{}, err
	}
	done := m.startCancellation(job)
	select {
	case <-done:
		return m.Status(jobID)
	case <-ctx.Done():
		return StatusResult{}, ctx.Err()
	}
}

func (m *Manager) drain() {
	for {
		m.mu.Lock()
		if m.active >= m.config.Limits.MaxConcurrentJobs {
			m.mu.Unlock()
			return
		}
		index := -1
		for i, id := range m.queue {
			job := m.jobs[id]
			if job != nil && job.record.State == StateQueued && m.canReserveLocked(job) {
				index = i
				break
			}
		}
		if index < 0 {
			m.mu.Unlock()
			return
		}
		id := m.queue[index]
		m.queue = append(m.queue[:index], m.queue[index+1:]...)
		job := m.jobs[id]
		m.reserveLocked(job)
		_ = transitionRecord(&job.record, StateStarting)
		now := m.config.Now()
		job.record.StartedAt = &now
		m.mu.Unlock()
		go m.begin(job)
	}
}

func (m *Manager) begin(job *managedJob) {
	if err := m.persistJob(job); err != nil {
		m.failJob(job, err)
		return
	}
	_ = m.appendOutput(job, RuntimeOutputEvent{Stream: "event", Text: "STARTING\n"})

	m.mu.Lock()
	if job.record.TimeoutMillis > 0 && !job.record.State.Terminal() {
		delay := time.Duration(job.record.TimeoutMillis) * time.Millisecond
		job.timer = time.AfterFunc(delay, func() { m.timeout(job) })
	}
	m.mu.Unlock()

	handle, err := m.executor.Execute(context.Background(), RuntimeExecutionRequest{
		JobID: job.record.JobID, Workspace: job.record.Workspace, Tool: job.record.Tool,
		Arguments: cloneMap(job.arguments), PermissionMode: job.record.PermissionMode,
		OnOutput:   func(event RuntimeOutputEvent) error { return m.appendOutput(job, event) },
		OnProgress: func(metadata map[string]any) { m.setProgress(job, metadata) },
	})
	if err != nil {
		m.mu.RLock()
		cancelling := job.record.State == StateCancelRequested
		m.mu.RUnlock()
		if cancelling {
			m.finishCancellation(job)
			return
		}
		m.failJob(job, err)
		return
	}
	if handle.Completion == nil {
		m.failJob(job, errors.New("runtime returned nil completion channel"))
		return
	}

	m.mu.Lock()
	job.record.RuntimeID = handle.RuntimeID
	state := job.record.State
	if state == StateStarting {
		_ = transitionRecord(&job.record, StateRunning)
	}
	m.mu.Unlock()
	_ = m.persistJob(job)
	_ = m.appendOutput(job, RuntimeOutputEvent{Stream: "event", Text: "RUNNING\n"})
	if state == StateCancelRequested {
		m.startCancellation(job)
	}

	outcome, ok := <-handle.Completion
	if !ok {
		outcome = RuntimeExecutionResult{Err: errors.New("runtime completion channel closed without a result")}
	}
	m.mu.RLock()
	cancelling := job.record.State == StateCancelRequested
	terminal := job.record.State.Terminal()
	m.mu.RUnlock()
	if terminal {
		return
	}
	if cancelling {
		m.finishCancellation(job)
		return
	}
	if outcome.Err != nil {
		m.failJob(job, outcome.Err)
		return
	}
	m.completeJob(job, outcome)
}

func (m *Manager) completeJob(job *managedJob, result RuntimeExecutionResult) {
	if result.Summary != "" {
		_ = m.appendOutput(job, RuntimeOutputEvent{Stream: "stdout", Text: result.Summary + "\n"})
	}
	m.mu.Lock()
	if job.record.State != StateRunning {
		m.mu.Unlock()
		return
	}
	exit := result.ExitCode
	job.record.ExitCode = &exit
	if result.Summary != "" {
		job.record.ResultSummary = truncateUTF8(m.config.RedactText(result.Summary), m.config.Limits.MaxResultSummaryBytes)
	} else if result.Result != nil {
		job.record.ResultSummary = summarize(result.Result, m.config.Limits.MaxResultSummaryBytes, m.config.RedactText)
	}
	target := StateSucceeded
	if !result.OK || result.ExitCode != 0 {
		target = StateFailed
		if job.record.Error == "" {
			job.record.Error = "runtime returned a non-success result"
		}
	}
	_ = transitionRecord(&job.record, target)
	now := m.config.Now()
	job.record.FinishedAt = &now
	job.arguments = nil
	m.stopTimerLocked(job)
	released := m.releaseLocked(job)
	m.mu.Unlock()
	_ = m.persistJob(job)
	if released {
		go m.drain()
	}
}

func (m *Manager) failJob(job *managedJob, failure error) {
	_ = m.appendOutput(job, RuntimeOutputEvent{Stream: "stderr", Text: m.config.RedactText(failure.Error()) + "\n"})
	m.mu.Lock()
	if job.record.State == StateCancelRequested {
		m.mu.Unlock()
		m.finishCancellation(job)
		return
	}
	if job.record.State != StateStarting && job.record.State != StateRunning {
		m.mu.Unlock()
		return
	}
	_ = transitionRecord(&job.record, StateFailed)
	job.record.Error = truncateUTF8(m.config.RedactText(failure.Error()), 4096)
	now := m.config.Now()
	job.record.FinishedAt = &now
	job.arguments = nil
	m.stopTimerLocked(job)
	released := m.releaseLocked(job)
	m.mu.Unlock()
	_ = m.persistJob(job)
	if released {
		go m.drain()
	}
}

func (m *Manager) timeout(job *managedJob) {
	m.mu.Lock()
	if job.record.State.Terminal() || job.record.State == StateQueued {
		m.mu.Unlock()
		return
	}
	job.timeoutTriggered = true
	job.record.CancelReason = "job timeout"
	if job.record.State != StateCancelRequested {
		_ = transitionRecord(&job.record, StateCancelRequested)
	}
	m.mu.Unlock()
	_ = m.persistJob(job)
	m.startCancellation(job)
}

func (m *Manager) startCancellation(job *managedJob) <-chan struct{} {
	m.mu.Lock()
	if job.record.State.Terminal() {
		done := make(chan struct{})
		close(done)
		m.mu.Unlock()
		return done
	}
	if job.cancelStarted {
		done := job.cancelDone
		m.mu.Unlock()
		return done
	}
	job.cancelStarted = true
	job.cancelErr = nil
	job.cancelDone = make(chan struct{})
	done := job.cancelDone
	request := RuntimeCancelRequest{JobID: job.record.JobID, RuntimeID: job.record.RuntimeID, Reason: job.record.CancelReason}
	m.mu.Unlock()
	go func() {
		err := m.executor.Cancel(context.Background(), request)
		if err == nil {
			m.finishCancellation(job)
		} else {
			m.mu.Lock()
			if job.record.State == StateCancelRequested {
				job.record.Error = truncateUTF8(m.config.RedactText("cancellation failed: "+err.Error()), 4096)
			}
			job.cancelErr = err
			job.cancelStarted = false
			m.mu.Unlock()
			_ = m.persistJob(job)
		}
		m.mu.Lock()
		if job.cancelDone == done {
			close(done)
			job.cancelDone = nil
		}
		m.mu.Unlock()
	}()
	return done
}

func (m *Manager) finishCancellation(job *managedJob) {
	m.mu.Lock()
	if job.record.State != StateCancelRequested {
		m.mu.Unlock()
		return
	}
	target := StateCancelled
	if job.timeoutTriggered {
		target = StateTimedOut
	}
	_ = transitionRecord(&job.record, target)
	now := m.config.Now()
	job.record.FinishedAt = &now
	job.arguments = nil
	m.stopTimerLocked(job)
	released := m.releaseLocked(job)
	m.mu.Unlock()
	_ = m.appendOutput(job, RuntimeOutputEvent{Stream: "event", Text: string(target) + "\n"})
	_ = m.persistJob(job)
	if released {
		go m.drain()
	}
}

func (m *Manager) appendOutput(job *managedJob, event RuntimeOutputEvent) error {
	if err := job.output.append(event); err != nil {
		return err
	}
	cursor, bytes := job.output.cursorAndBytes()
	m.mu.Lock()
	job.record.OutputCursor = cursor
	job.record.OutputBytes = bytes
	m.mu.Unlock()
	return nil
}

func (m *Manager) setProgress(job *managedJob, metadata map[string]any) {
	if metadata == nil {
		return
	}
	summary := summarize(metadata, m.config.Limits.MaxMetadataSummaryBytes, m.config.RedactText)
	jobProgress := map[string]any{"summary": summary}
	m.mu.Lock()
	job.progress = jobProgress
	m.mu.Unlock()
}

func (m *Manager) persistJob(job *managedJob) error {
	job.persistMu.Lock()
	defer job.persistMu.Unlock()
	m.mu.RLock()
	snapshot := job.record
	m.mu.RUnlock()
	return m.persistence.save(snapshot)
}

func (m *Manager) canReserveLocked(job *managedJob) bool {
	if m.activeByWorkspace[job.workspaceKey] >= m.config.Limits.MaxConcurrentPerWorkspace {
		return false
	}
	if job.record.ConcurrencyClass != ConcurrencyRead && m.writeByWorkspace[job.workspaceKey] >= m.config.Limits.MaxConcurrentWritePerWorkspace {
		return false
	}
	return true
}

func (m *Manager) reserveLocked(job *managedJob) {
	if job.slotReserved {
		return
	}
	job.slotReserved = true
	m.active++
	m.activeByWorkspace[job.workspaceKey]++
	if job.record.ConcurrencyClass != ConcurrencyRead {
		m.writeByWorkspace[job.workspaceKey]++
	}
}

func (m *Manager) releaseLocked(job *managedJob) bool {
	if !job.slotReserved {
		return false
	}
	job.slotReserved = false
	if m.active > 0 {
		m.active--
	}
	decrement(m.activeByWorkspace, job.workspaceKey)
	if job.record.ConcurrencyClass != ConcurrencyRead {
		decrement(m.writeByWorkspace, job.workspaceKey)
	}
	return true
}

func decrement(values map[string]int, key string) {
	values[key]--
	if values[key] <= 0 {
		delete(values, key)
	}
}

func (m *Manager) removeQueuedLocked(jobID string) {
	for i, id := range m.queue {
		if id == jobID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

func (m *Manager) stopTimerLocked(job *managedJob) {
	if job.timer != nil {
		job.timer.Stop()
		job.timer = nil
	}
}

func (m *Manager) statusLocked(job *managedJob) StatusResult {
	end := m.config.Now()
	if job.record.FinishedAt != nil {
		end = *job.record.FinishedAt
	}
	start := job.record.CreatedAt
	if job.record.StartedAt != nil {
		start = *job.record.StartedAt
	}
	progress := map[string]any(nil)
	if job.progress != nil {
		progress = make(map[string]any, len(job.progress))
		for k, v := range job.progress {
			progress[k] = v
		}
	}
	return StatusResult{
		JobID: job.record.JobID, Workspace: job.record.Workspace, Tool: job.record.Tool, PermissionMode: job.record.PermissionMode,
		State: job.record.State, CreatedAt: job.record.CreatedAt, StartedAt: job.record.StartedAt, FinishedAt: job.record.FinishedAt,
		DurationMS: maxInt64(0, end.Sub(start).Milliseconds()), RuntimeID: job.record.RuntimeID, OutputCursor: job.record.OutputCursor,
		OutputBytes: job.record.OutputBytes, ExitCode: job.record.ExitCode, ResultSummary: job.record.ResultSummary, Error: job.record.Error,
		CancelReason: job.record.CancelReason, ProgressMetadata: progress,
	}
}

func (m *Manager) recover() error {
	records, err := m.persistence.loadAll()
	if err != nil {
		return err
	}
	for _, record := range records {
		if !knownState(record.State) {
			continue
		}
		if record.PermissionMode == "" {
			record.PermissionMode = "project"
		}
		if record.ConcurrencyClass == "" {
			record.ConcurrencyClass = ConcurrencyWrite
		}
		dir, _ := m.persistence.jobDir(record.JobID)
		output := newOutputStore(dir, m.config.Limits, m.config.RedactText, m.config.Now)
		if err := output.initialize(record.OutputCursor, record.OutputBytes); err != nil {
			return err
		}
		record.OutputCursor, record.OutputBytes = output.cursorAndBytes()
		job := &managedJob{record: record, output: output, workspaceKey: workspaceKey(record.Workspace)}
		if !record.State.Terminal() {
			if err := transitionRecord(&job.record, StateInterrupted); err != nil {
				return err
			}
			now := m.config.Now()
			job.record.FinishedAt = &now
			job.record.Error = "AgentDock restarted; runtime reattach is unavailable in Phase C1-C"
		}
		m.jobs[job.record.JobID] = job
		if err := m.persistence.save(job.record); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) cleanupRetention() error {
	m.mu.Lock()
	now := m.config.Now()
	type terminalItem struct {
		id string
		at time.Time
	}
	var terminal []terminalItem
	for id, job := range m.jobs {
		if !job.record.State.Terminal() || job.slotReserved {
			continue
		}
		at := job.record.CreatedAt
		if job.record.FinishedAt != nil {
			at = *job.record.FinishedAt
		}
		terminal = append(terminal, terminalItem{id: id, at: at})
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].at.After(terminal[j].at) })
	var remove []string
	for i, item := range terminal {
		if now.Sub(item.at) > m.config.Limits.Retention || i >= m.config.Limits.RetentionMaxCompletedJobs {
			remove = append(remove, item.id)
			delete(m.jobs, item.id)
		}
	}
	m.mu.Unlock()
	for _, id := range remove {
		if err := m.persistence.remove(id); err != nil {
			return err
		}
	}
	return nil
}

func transitionRecord(record *Record, next State) error {
	if err := ValidateTransition(record.State, next); err != nil {
		return err
	}
	record.State = next
	return nil
}

func knownState(state State) bool {
	_, ok := allowedTransitions[state]
	return ok
}

func workspaceKey(value string) string {
	resolved, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		resolved = filepath.Clean(strings.TrimSpace(value))
	}
	resolved = filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	return resolved
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneValue(item)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = cloneValue(v)
		}
		return out
	default:
		return typed
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
