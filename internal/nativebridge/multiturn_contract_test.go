package nativebridge

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

const (
	d3F0ResumeSemantics              = "MESSAGE_CONTINUE_ONLY"
	d3F0TrueInterruptedResumeSupport = false
)

type f0Message struct {
	ClientID     string
	SupervisorID string
	Seq          int64
	Kind         string
	Terminal     string
	Dispatched   bool
}

type f0Approval struct {
	RequestID       string
	NativeRequestID string
	SupervisorID    string
	MessageSeq      int64
	ApprovalSeq     int64
	EventCursor     int64
	ClientDecision  string
	Decision        string
	Delivered       bool
}

type f0Snapshot struct {
	NextMessageSeq  int64
	NextApprovalSeq int64
	EventCursor     int64
	Messages        []f0Message
	Approvals       []f0Approval
	EventIDs        []string
}

type f0Ledger struct {
	mu sync.Mutex

	nextMessageSeq  int64
	nextApprovalSeq int64
	eventCursor     int64

	messagesByClient  map[string]f0Message
	clientBySupID     map[string]string
	approvalsByNative map[string]f0Approval
	nativeByApproval  map[string]string
	eventIDs          map[string]struct{}

	dispatchCount         map[string]int
	approvalDeliveryCount map[string]int
	abortCount            int
}

func newF0Ledger() *f0Ledger {
	return &f0Ledger{
		messagesByClient:      map[string]f0Message{},
		clientBySupID:         map[string]string{},
		approvalsByNative:     map[string]f0Approval{},
		nativeByApproval:      map[string]string{},
		eventIDs:              map[string]struct{}{},
		dispatchCount:         map[string]int{},
		approvalDeliveryCount: map[string]int{},
	}
}

func restoreF0Ledger(snapshot f0Snapshot) *f0Ledger {
	l := newF0Ledger()
	l.nextMessageSeq = snapshot.NextMessageSeq
	l.nextApprovalSeq = snapshot.NextApprovalSeq
	l.eventCursor = snapshot.EventCursor
	for _, msg := range snapshot.Messages {
		l.messagesByClient[msg.ClientID] = msg
		l.clientBySupID[msg.SupervisorID] = msg.ClientID
	}
	for _, approval := range snapshot.Approvals {
		l.approvalsByNative[approval.NativeRequestID] = approval
		l.nativeByApproval[approval.RequestID] = approval.NativeRequestID
	}
	for _, id := range snapshot.EventIDs {
		l.eventIDs[id] = struct{}{}
	}
	return l
}

func (l *f0Ledger) snapshot() f0Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := f0Snapshot{
		NextMessageSeq:  l.nextMessageSeq,
		NextApprovalSeq: l.nextApprovalSeq,
		EventCursor:     l.eventCursor,
	}
	for _, msg := range l.messagesByClient {
		s.Messages = append(s.Messages, msg)
	}
	sort.Slice(s.Messages, func(i, j int) bool { return s.Messages[i].Seq < s.Messages[j].Seq })
	for _, approval := range l.approvalsByNative {
		s.Approvals = append(s.Approvals, approval)
	}
	sort.Slice(s.Approvals, func(i, j int) bool { return s.Approvals[i].ApprovalSeq < s.Approvals[j].ApprovalSeq })
	for id := range l.eventIDs {
		s.EventIDs = append(s.EventIDs, id)
	}
	sort.Strings(s.EventIDs)
	return s
}

func (l *f0Ledger) admit(clientID, kind string) (f0Message, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.messagesByClient[clientID]; ok {
		return existing, false
	}
	l.nextMessageSeq++
	msg := f0Message{
		ClientID:     clientID,
		SupervisorID: fmt.Sprintf("supmsg-%06d", l.nextMessageSeq),
		Seq:          l.nextMessageSeq,
		Kind:         kind,
	}
	l.messagesByClient[clientID] = msg
	l.clientBySupID[msg.SupervisorID] = clientID
	return msg, true
}

func (l *f0Ledger) dispatch(clientID string) f0Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := l.messagesByClient[clientID]
	if !msg.Dispatched {
		msg.Dispatched = true
		l.messagesByClient[clientID] = msg
		l.dispatchCount[msg.SupervisorID]++
	}
	return msg
}

func (l *f0Ledger) terminal(clientID, outcome string) f0Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := l.messagesByClient[clientID]
	msg.Terminal = outcome
	l.messagesByClient[clientID] = msg
	return msg
}

func (l *f0Ledger) busyCorrection(clientID string) f0Message {
	msg, _ := l.admit(clientID, "CORRECTION")
	return l.dispatch(msg.ClientID)
}

func (l *f0Ledger) observeEvent(eventID string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, seen := l.eventIDs[eventID]; seen {
		return l.eventCursor
	}
	l.eventIDs[eventID] = struct{}{}
	l.eventCursor++
	return l.eventCursor
}

func (l *f0Ledger) requestApproval(supervisorID, nativeRequestID string, eventCursor int64) (f0Approval, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.approvalsByNative[nativeRequestID]; ok {
		return existing, false, nil
	}
	clientID, ok := l.clientBySupID[supervisorID]
	if !ok {
		return f0Approval{}, false, errors.New("parent supervisor message must be durable first")
	}
	parent := l.messagesByClient[clientID]
	l.nextApprovalSeq++
	approval := f0Approval{
		RequestID:       fmt.Sprintf("approval-%06d", l.nextApprovalSeq),
		NativeRequestID: nativeRequestID,
		SupervisorID:    supervisorID,
		MessageSeq:      parent.Seq,
		ApprovalSeq:     l.nextApprovalSeq,
		EventCursor:     eventCursor,
	}
	l.approvalsByNative[nativeRequestID] = approval
	l.nativeByApproval[approval.RequestID] = nativeRequestID
	return approval, true, nil
}

func (l *f0Ledger) decideApproval(requestID, clientDecisionID, decision string) (f0Approval, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nativeID, ok := l.nativeByApproval[requestID]
	if !ok {
		return f0Approval{}, false, errors.New("approval not found")
	}
	approval := l.approvalsByNative[nativeID]
	if approval.Decision != "" {
		if approval.Decision == decision {
			return approval, false, nil
		}
		return approval, false, errors.New("conflicting approval decision")
	}
	approval.ClientDecision = clientDecisionID
	approval.Decision = decision
	approval.Delivered = true
	l.approvalsByNative[nativeID] = approval
	l.approvalDeliveryCount[requestID]++
	return approval, true, nil
}

func TestD3F0FrozenResumeSemantics(t *testing.T) {
	if d3F0TrueInterruptedResumeSupport {
		t.Fatal("TRUE_INTERRUPTED_EXECUTION_RESUME must remain NOT_SUPPORTED")
	}
	if d3F0ResumeSemantics != "MESSAGE_CONTINUE_ONLY" {
		t.Fatalf("resume semantics = %q", d3F0ResumeSemantics)
	}
}

func TestD3F0TaskResponseFollowupSameConversationContract(t *testing.T) {
	l := newF0Ledger()
	task, _ := l.admit("client-task", "TASK")
	l.dispatch(task.ClientID)
	l.terminal(task.ClientID, "SUCCEEDED")

	followup, created := l.admit("client-followup", "FOLLOWUP")
	if !created {
		t.Fatal("follow-up must be a new logical supervisor message")
	}
	if followup.Seq != task.Seq+1 {
		t.Fatalf("follow-up seq = %d, want %d", followup.Seq, task.Seq+1)
	}
	if followup.SupervisorID == task.SupervisorID {
		t.Fatal("follow-up must have a distinct supervisor_message_id")
	}
}

func TestD3F0BusyCorrectionIsOrderedAndNonPreemptive(t *testing.T) {
	l := newF0Ledger()
	task, _ := l.admit("client-task", "TASK")
	l.dispatch(task.ClientID)

	correction := l.busyCorrection("client-correction")
	if correction.Seq != task.Seq+1 {
		t.Fatalf("correction seq = %d, want %d", correction.Seq, task.Seq+1)
	}
	if correction.Kind != "CORRECTION" {
		t.Fatalf("correction kind = %q", correction.Kind)
	}
	if l.abortCount != 0 {
		t.Fatalf("busy correction implicitly aborted active turn: abortCount=%d", l.abortCount)
	}
}

func TestD3F0TerminalOutcomesAllowSameSessionFollowup(t *testing.T) {
	for _, outcome := range []string{"SUCCEEDED", "FAILED", "CANCELLED", "TIMED_OUT", "INTERRUPTED"} {
		t.Run(outcome, func(t *testing.T) {
			l := newF0Ledger()
			first, _ := l.admit("first", "TASK")
			l.dispatch(first.ClientID)
			l.terminal(first.ClientID, outcome)
			next, created := l.admit("next", "FOLLOWUP")
			if !created || next.Seq != 2 {
				t.Fatalf("follow-up after %s was not admitted on the persistent conversation: %#v", outcome, next)
			}
		})
	}
}

func TestD3F0DuplicateClientMessageExactlyOnce(t *testing.T) {
	l := newF0Ledger()
	first, created := l.admit("dup", "TASK")
	if !created {
		t.Fatal("first admission was not created")
	}
	l.dispatch(first.ClientID)

	second, created := l.admit("dup", "TASK")
	if created {
		t.Fatal("duplicate admission created a second logical message")
	}
	l.dispatch(second.ClientID)

	if second.SupervisorID != first.SupervisorID || second.Seq != first.Seq {
		t.Fatalf("duplicate identity changed: first=%#v second=%#v", first, second)
	}
	if got := l.dispatchCount[first.SupervisorID]; got != 1 {
		t.Fatalf("native dispatch count = %d, want 1", got)
	}
}

func TestD3F0ConcurrentDuplicateExactlyOnce(t *testing.T) {
	l := newF0Ledger()
	const n = 32
	results := make(chan f0Message, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			msg, _ := l.admit("same-client-id", "TASK")
			l.dispatch(msg.ClientID)
			results <- msg
		}()
	}
	wg.Wait()
	close(results)

	var first f0Message
	for msg := range results {
		if first.SupervisorID == "" {
			first = msg
			continue
		}
		if msg.SupervisorID != first.SupervisorID || msg.Seq != first.Seq {
			t.Fatalf("concurrent duplicate diverged: first=%#v got=%#v", first, msg)
		}
	}
	if got := l.dispatchCount[first.SupervisorID]; got != 1 {
		t.Fatalf("concurrent duplicate dispatch count = %d, want 1", got)
	}
}

func TestD3F0TwoSimultaneousSupervisorMessagesHaveDurableTotalOrder(t *testing.T) {
	l := newF0Ledger()
	start := make(chan struct{})
	out := make(chan f0Message, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"alpha", "beta"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			msg, _ := l.admit(id, "FOLLOWUP")
			out <- msg
		}()
	}
	close(start)
	wg.Wait()
	close(out)

	var seqs []int64
	for msg := range out {
		seqs = append(seqs, msg.Seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	if fmt.Sprint(seqs) != "[1 2]" {
		t.Fatalf("simultaneous messages did not linearize to one total order: %v", seqs)
	}

	restarted := restoreF0Ledger(l.snapshot())
	snap := restarted.snapshot()
	if len(snap.Messages) != 2 || snap.Messages[0].Seq != 1 || snap.Messages[1].Seq != 2 {
		t.Fatalf("restart changed canonical message order: %#v", snap.Messages)
	}
}

func TestD3F0RestartPreservesDedupIdentityAndNextSequence(t *testing.T) {
	l := newF0Ledger()
	first, _ := l.admit("m1", "TASK")
	second, _ := l.admit("m2", "FOLLOWUP")
	restarted := restoreF0Ledger(l.snapshot())

	dup, created := restarted.admit("m1", "TASK")
	if created || dup.SupervisorID != first.SupervisorID || dup.Seq != first.Seq {
		t.Fatalf("restart lost duplicate identity: %#v", dup)
	}
	third, created := restarted.admit("m3", "FOLLOWUP")
	if !created || third.Seq != second.Seq+1 {
		t.Fatalf("restart lost next sequence: %#v", third)
	}
}

func TestD3F0OwnedEventCursorIsDuplicateSafe(t *testing.T) {
	l := newF0Ledger()
	if got := l.observeEvent("evt-1"); got != 1 {
		t.Fatalf("cursor after first event = %d", got)
	}
	if got := l.observeEvent("evt-1"); got != 1 {
		t.Fatalf("duplicate event advanced cursor to %d", got)
	}
	if got := l.observeEvent("evt-2"); got != 2 {
		t.Fatalf("cursor after second unique event = %d", got)
	}
}

func TestD3F0ApprovalRequestOrderingAndDuplicateProtection(t *testing.T) {
	l := newF0Ledger()
	if _, _, err := l.requestApproval("missing-parent", "native-1", 1); err == nil {
		t.Fatal("approval request was admitted before its parent supervisor message")
	}
	msg, _ := l.admit("task", "TASK")
	cursor := l.observeEvent("permission-event")
	first, created, err := l.requestApproval(msg.SupervisorID, "native-1", cursor)
	if err != nil || !created {
		t.Fatalf("first approval request failed: created=%v err=%v", created, err)
	}
	second, created, err := l.requestApproval(msg.SupervisorID, "native-1", cursor)
	if err != nil || created {
		t.Fatalf("duplicate approval request was not idempotent: created=%v err=%v", created, err)
	}
	if first.RequestID != second.RequestID || first.ApprovalSeq != second.ApprovalSeq || first.MessageSeq != msg.Seq {
		t.Fatalf("approval identity/order changed: first=%#v second=%#v parent=%#v", first, second, msg)
	}
}

func TestD3F0ApprovalDecisionIsBoundedExactlyOnceAndRestartSafe(t *testing.T) {
	l := newF0Ledger()
	msg, _ := l.admit("task", "TASK")
	cursor := l.observeEvent("permission-event")
	approval, _, err := l.requestApproval(msg.SupervisorID, "native-approval", cursor)
	if err != nil {
		t.Fatal(err)
	}

	first, applied, err := l.decideApproval(approval.RequestID, "decision-1", "ALLOW")
	if err != nil || !applied {
		t.Fatalf("first approval decision failed: applied=%v err=%v", applied, err)
	}
	second, applied, err := l.decideApproval(approval.RequestID, "decision-1", "ALLOW")
	if err != nil || applied {
		t.Fatalf("duplicate decision was not idempotent: applied=%v err=%v", applied, err)
	}
	if second.Decision != first.Decision || l.approvalDeliveryCount[approval.RequestID] != 1 {
		t.Fatalf("duplicate decision changed/delivered twice: first=%#v second=%#v deliveries=%d", first, second, l.approvalDeliveryCount[approval.RequestID])
	}
	if _, _, err := l.decideApproval(approval.RequestID, "decision-2", "DENY"); err == nil {
		t.Fatal("conflicting later approval decision was accepted")
	}

	restarted := restoreF0Ledger(l.snapshot())
	afterRestart, applied, err := restarted.decideApproval(approval.RequestID, "decision-3", "ALLOW")
	if err != nil || applied {
		t.Fatalf("restart lost terminal approval idempotency: applied=%v err=%v", applied, err)
	}
	if afterRestart.Decision != "ALLOW" || afterRestart.MessageSeq != msg.Seq {
		t.Fatalf("restart changed approval decision/order: %#v", afterRestart)
	}
}
