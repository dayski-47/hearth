package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	hv1 "github.com/dayski-47/hearth/gateway/internal/hearth/v1"
	"github.com/dayski-47/hearth/gateway/internal/store"
	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc"
)

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	u, err := store.ParseUUID(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

func strptr(s string) *string { return &s }

// --- fake store -----------------------------------------------------------

type fakeStore struct {
	mu           sync.Mutex
	rows         []gen.Workspace
	listErr      error
	states       []gen.SetWorkspaceStateParams
	events       []gen.AppendWorkspaceEventParams
	markedAgents []string
	markIDs      []pgtype.UUID
}

func (f *fakeStore) ListReconcilableWorkspaces(context.Context) ([]gen.Workspace, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *fakeStore) SetWorkspaceState(_ context.Context, p gen.SetWorkspaceStateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, p)
	return nil
}

func (f *fakeStore) AppendWorkspaceEvent(_ context.Context, p gen.AppendWorkspaceEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, p)
	return nil
}

func (f *fakeStore) MarkAgentWorkspacesUnknown(_ context.Context, agentID *string) ([]pgtype.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if agentID != nil {
		f.markedAgents = append(f.markedAgents, *agentID)
	}
	return f.markIDs, nil
}

func (f *fakeStore) eventKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	for i, e := range f.events {
		out[i] = e.Kind
	}
	return out
}

// --- fake registry ------------------------------------------------------

type fakeRegistry struct {
	addrs map[string]string
	lost  []string
}

func (r fakeRegistry) Addr(id string) (string, bool) {
	a, ok := r.addrs[id]
	return a, ok
}

func (r fakeRegistry) LostAgents() []string { return r.lost }

// --- fake agent client + dialer ---------------------------------------

type fakeAgentClient struct {
	getFn func(context.Context, *hv1.WorkspaceRef) (*hv1.Workspace, error)
}

func (c *fakeAgentClient) CreateWorkspace(context.Context, *hv1.CreateWorkspaceRequest, ...grpc.CallOption) (*hv1.Workspace, error) {
	return nil, errors.New("unused")
}
func (c *fakeAgentClient) StartWorkspace(context.Context, *hv1.WorkspaceRef, ...grpc.CallOption) (*hv1.Workspace, error) {
	return nil, errors.New("unused")
}
func (c *fakeAgentClient) StopWorkspace(context.Context, *hv1.WorkspaceRef, ...grpc.CallOption) (*hv1.Workspace, error) {
	return nil, errors.New("unused")
}
func (c *fakeAgentClient) DestroyWorkspace(context.Context, *hv1.WorkspaceRef, ...grpc.CallOption) (*hv1.DestroyResponse, error) {
	return nil, errors.New("unused")
}
func (c *fakeAgentClient) GetWorkspace(ctx context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if c.getFn != nil {
		return c.getFn(ctx, in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
}

type fakeDialer struct {
	client  hv1.AgentClient
	dialErr error
	dialed  []string
	closed  int
}

type nopCloser struct{ d *fakeDialer }

func (c nopCloser) Close() error { c.d.closed++; return nil }

func (d *fakeDialer) Dial(addr string) (hv1.AgentClient, io.Closer, error) {
	d.dialed = append(d.dialed, addr)
	if d.dialErr != nil {
		return nil, nil, d.dialErr
	}
	return d.client, nopCloser{d}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- tests ------------------------------------------------------------

func TestConvergeUpdatesDriftedState(t *testing.T) {
	id := mustUUID(t, "00000000-0000-0000-0000-000000000001")
	st := &fakeStore{rows: []gen.Workspace{
		{ID: id, State: "running", AgentID: strptr("h1")},
	}}
	dial := &fakeDialer{client: &fakeAgentClient{
		getFn: func(_ context.Context, in *hv1.WorkspaceRef) (*hv1.Workspace, error) {
			return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_STOPPED}, nil
		},
	}}
	reg := fakeRegistry{addrs: map[string]string{"h1": "https://h1:9091"}}

	if err := Converge(context.Background(), Deps{Store: st, Dial: dial, Reg: reg, Logger: testLogger()}); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(st.states) != 1 || st.states[0].State != "stopped" || store.UUIDString(st.states[0].ID) != store.UUIDString(id) {
		t.Fatalf("states = %+v, want one stopped write for %s", st.states, store.UUIDString(id))
	}
	if !contains(st.eventKinds(), "reconciled") {
		t.Fatalf("events = %v, want reconciled", st.eventKinds())
	}
	if dial.closed != len(dial.dialed) {
		t.Fatalf("closed %d conns, dialed %d", dial.closed, len(dial.dialed))
	}
}

func TestConvergeNoEventWhenInSync(t *testing.T) {
	id := mustUUID(t, "00000000-0000-0000-0000-000000000002")
	st := &fakeStore{rows: []gen.Workspace{
		{ID: id, State: "running", AgentID: strptr("h1")},
	}}
	dial := &fakeDialer{client: &fakeAgentClient{
		getFn: func(_ context.Context, in *hv1.WorkspaceRef) (*hv1.Workspace, error) {
			return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
		},
	}}
	reg := fakeRegistry{addrs: map[string]string{"h1": "https://h1:9091"}}

	if err := Converge(context.Background(), Deps{Store: st, Dial: dial, Reg: reg, Logger: testLogger()}); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(st.states) != 0 {
		t.Fatalf("states = %+v, want no writes", st.states)
	}
	if len(st.events) != 0 {
		t.Fatalf("events = %v, want none", st.eventKinds())
	}
}

func TestConvergeMarksLostAgentWorkspacesUnknown(t *testing.T) {
	live := mustUUID(t, "00000000-0000-0000-0000-000000000003")
	onLost := mustUUID(t, "00000000-0000-0000-0000-000000000004")
	st := &fakeStore{
		markIDs: []pgtype.UUID{onLost},
		rows: []gen.Workspace{
			{ID: live, State: "running", AgentID: strptr("h1")},
			{ID: onLost, State: "running", AgentID: strptr("h2")},
		},
	}
	dial := &fakeDialer{client: &fakeAgentClient{}}
	reg := fakeRegistry{
		addrs: map[string]string{"h1": "https://h1:9091", "h2": "https://h2:9091"},
		lost:  []string{"h2"},
	}

	if err := Converge(context.Background(), Deps{Store: st, Dial: dial, Reg: reg, Logger: testLogger()}); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(st.markedAgents) != 1 || st.markedAgents[0] != "h2" {
		t.Fatalf("markedAgents = %v, want [h2]", st.markedAgents)
	}
	// h2's row must not be polled; only h1 dialed.
	if len(dial.dialed) != 1 || dial.dialed[0] != "https://h1:9091" {
		t.Fatalf("dialed = %v, want only h1", dial.dialed)
	}
	// The bulk mark is a state change, so it owes every affected row an event.
	if len(st.events) != 1 || st.events[0].Kind != "agent_lost" ||
		store.UUIDString(st.events[0].WorkspaceID) != store.UUIDString(onLost) {
		t.Fatalf("events = %+v, want one agent_lost for %s", st.events, store.UUIDString(onLost))
	}
	var detail map[string]string
	if err := json.Unmarshal(st.events[0].Detail, &detail); err != nil {
		t.Fatalf("decode event detail: %v", err)
	}
	if detail["agent_id"] != "h2" {
		t.Fatalf("event detail = %v, want agent_id h2", detail)
	}
}

func TestConvergeReconcilesCreatingRow(t *testing.T) {
	// A create interrupted after the agent call leaves a "creating" row with a
	// live container; the next pass must converge it, not ignore it.
	id := mustUUID(t, "00000000-0000-0000-0000-000000000006")
	st := &fakeStore{rows: []gen.Workspace{
		{ID: id, State: "creating", AgentID: strptr("h1")},
	}}
	dial := &fakeDialer{client: &fakeAgentClient{
		getFn: func(_ context.Context, in *hv1.WorkspaceRef) (*hv1.Workspace, error) {
			return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
		},
	}}
	reg := fakeRegistry{addrs: map[string]string{"h1": "https://h1:9091"}}

	if err := Converge(context.Background(), Deps{Store: st, Dial: dial, Reg: reg, Logger: testLogger()}); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(st.states) != 1 || st.states[0].State != "running" {
		t.Fatalf("states = %+v, want one running write", st.states)
	}
	if !contains(st.eventKinds(), "reconciled") {
		t.Fatalf("events = %v, want reconciled", st.eventKinds())
	}
}

func TestConvergeSkipsRowOnDialError(t *testing.T) {
	id := mustUUID(t, "00000000-0000-0000-0000-000000000005")
	st := &fakeStore{rows: []gen.Workspace{
		{ID: id, State: "running", AgentID: strptr("h1")},
	}}
	dial := &fakeDialer{client: &fakeAgentClient{}, dialErr: errors.New("connection refused")}
	reg := fakeRegistry{addrs: map[string]string{"h1": "https://h1:9091"}}

	if err := Converge(context.Background(), Deps{Store: st, Dial: dial, Reg: reg, Logger: testLogger()}); err != nil {
		t.Fatalf("Converge returned error on dial failure: %v", err)
	}
	if len(st.states) != 0 || len(st.events) != 0 {
		t.Fatalf("row touched despite dial error: states=%+v events=%v", st.states, st.eventKinds())
	}
}
