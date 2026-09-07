package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/dayski-47/hearth/gateway/internal/agentregistry"
	"github.com/dayski-47/hearth/gateway/internal/config"
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

const (
	ownerAID = "11111111-1111-1111-1111-111111111111"
	ownerBID = "22222222-2222-2222-2222-222222222222"
)

// --- fake store -------------------------------------------------------------

type fakeStore struct {
	mu     sync.Mutex
	seq    int
	rows   map[string]gen.Workspace
	events []gen.AppendWorkspaceEventParams
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]gen.Workspace{}} }

func (f *fakeStore) get(id pgtype.UUID) (gen.Workspace, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, ok := f.rows[store.UUIDString(id)]
	return ws, ok
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

func (f *fakeStore) CreateWorkspace(_ context.Context, p gen.CreateWorkspaceParams) (gen.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := f.mkID()
	ws := gen.Workspace{
		ID: id, OwnerID: p.OwnerID, Name: p.Name, Image: p.Image,
		State: "creating", AgentID: p.AgentID,
	}
	f.rows[store.UUIDString(id)] = ws
	return ws, nil
}

func (f *fakeStore) mkID() pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(fmt.Sprintf("00000000-0000-0000-0000-%012d", f.seq)); err != nil {
		panic(err)
	}
	return u
}

func (f *fakeStore) GetWorkspaceForOwner(_ context.Context, p gen.GetWorkspaceForOwnerParams) (gen.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, ok := f.rows[store.UUIDString(p.ID)]
	if !ok || store.UUIDString(ws.OwnerID) != store.UUIDString(p.OwnerID) {
		return gen.Workspace{}, errors.New("no rows")
	}
	return ws, nil
}

func (f *fakeStore) ListWorkspacesForOwner(_ context.Context, ownerID pgtype.UUID) ([]gen.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gen.Workspace
	for _, ws := range f.rows {
		if store.UUIDString(ws.OwnerID) == store.UUIDString(ownerID) {
			out = append(out, ws)
		}
	}
	return out, nil
}

func (f *fakeStore) SetWorkspacePlacement(_ context.Context, p gen.SetWorkspacePlacementParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, ok := f.rows[store.UUIDString(p.ID)]
	if !ok {
		return errors.New("no rows")
	}
	ws.ContainerID = p.ContainerID
	ws.State = p.State
	f.rows[store.UUIDString(p.ID)] = ws
	return nil
}

func (f *fakeStore) SetWorkspaceState(_ context.Context, p gen.SetWorkspaceStateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws, ok := f.rows[store.UUIDString(p.ID)]
	if !ok {
		return errors.New("no rows")
	}
	ws.State = p.State
	f.rows[store.UUIDString(p.ID)] = ws
	return nil
}

func (f *fakeStore) DeleteWorkspace(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, store.UUIDString(id))
	return nil
}

func (f *fakeStore) AppendWorkspaceEvent(_ context.Context, p gen.AppendWorkspaceEventParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, p)
	return nil
}

// --- fake registry ---------------------------------------------------------

type fakeRegistry struct {
	agent   agentregistry.Agent
	pickErr error
}

func (r fakeRegistry) Pick(context.Context) (agentregistry.Agent, error) {
	if r.pickErr != nil {
		return agentregistry.Agent{}, r.pickErr
	}
	return r.agent, nil
}

func (r fakeRegistry) Addr(id string) (string, bool) {
	if r.pickErr == nil && id == r.agent.ID {
		return r.agent.AdvertiseAddr, true
	}
	return "", false
}

// --- fake agent client + dialer ------------------------------------------

type fakeAgentClient struct {
	createFn  func(context.Context, *hv1.CreateWorkspaceRequest) (*hv1.Workspace, error)
	startFn   func(context.Context, *hv1.WorkspaceRef) (*hv1.Workspace, error)
	stopFn    func(context.Context, *hv1.WorkspaceRef) (*hv1.Workspace, error)
	destroyFn func(context.Context, *hv1.WorkspaceRef) (*hv1.DestroyResponse, error)
	getFn     func(context.Context, *hv1.WorkspaceRef) (*hv1.Workspace, error)
}

func (c *fakeAgentClient) CreateWorkspace(ctx context.Context, in *hv1.CreateWorkspaceRequest, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if c.createFn != nil {
		return c.createFn(ctx, in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING, ContainerId: "cont-" + in.WorkspaceId}, nil
}

func (c *fakeAgentClient) StartWorkspace(ctx context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if c.startFn != nil {
		return c.startFn(ctx, in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_RUNNING}, nil
}

func (c *fakeAgentClient) StopWorkspace(ctx context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.Workspace, error) {
	if c.stopFn != nil {
		return c.stopFn(ctx, in)
	}
	return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_STOPPED}, nil
}

func (c *fakeAgentClient) DestroyWorkspace(ctx context.Context, in *hv1.WorkspaceRef, _ ...grpc.CallOption) (*hv1.DestroyResponse, error) {
	if c.destroyFn != nil {
		return c.destroyFn(ctx, in)
	}
	return &hv1.DestroyResponse{}, nil
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
}

func (d *fakeDialer) Dial(addr string) (hv1.AgentClient, io.Closer, error) {
	d.dialed = append(d.dialed, addr)
	if d.dialErr != nil {
		return nil, nil, d.dialErr
	}
	return d.client, io.NopCloser(nil), nil
}

// --- helpers -------------------------------------------------------------

func newTestService(t *testing.T, st Store, reg Registry, dial Dialer) *Service {
	t.Helper()
	return NewService(st, reg, dial, config.WorkspaceDefaults{
		Image:       "default:latest",
		Network:     "egress",
		CPUMillis:   2000,
		MemoryBytes: 1 << 30,
		Pids:        512,
		DiskBytes:   5 << 30,
		UserNS:      "keep-id",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func readyRegistry() fakeRegistry {
	return fakeRegistry{agent: agentregistry.Agent{
		ID: "h1", AdvertiseAddr: "https://h1:9091", Status: "ready",
	}}
}

// --- tests --------------------------------------------------------------

func TestCreatePlacesAndRunsWorkspace(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ws.State != "running" {
		t.Fatalf("returned state = %q, want running", ws.State)
	}
	if ws.Image != "default:latest" {
		t.Fatalf("image = %q, want default", ws.Image)
	}
	if ws.ContainerID == nil || *ws.ContainerID == "" {
		t.Fatalf("container id not set: %+v", ws.ContainerID)
	}
	stored, ok := st.get(ws.ID)
	if !ok || stored.State != "running" {
		t.Fatalf("stored row = %+v, ok=%v", stored, ok)
	}
	if stored.ContainerID == nil || *stored.ContainerID != *ws.ContainerID {
		t.Fatalf("stored container id = %v", stored.ContainerID)
	}
	kinds := st.eventKinds()
	if !contains(kinds, "created") || !contains(kinds, "running") {
		t.Fatalf("events = %v, want created + running", kinds)
	}
	if len(dial.dialed) != 1 || dial.dialed[0] != "https://h1:9091" {
		t.Fatalf("dialed = %v", dial.dialed)
	}
}

func TestCreateWithNoAgentReturnsErrNoAgent(t *testing.T) {
	st := newFakeStore()
	reg := fakeRegistry{pickErr: agentregistry.ErrNoAgent}
	s := newTestService(t, st, reg, &fakeDialer{client: &fakeAgentClient{}})

	_, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if !errors.Is(err, ErrNoAgent) {
		t.Fatalf("err = %v, want ErrNoAgent", err)
	}
	if len(st.rows) != 0 {
		t.Fatalf("expected no row inserted, got %d", len(st.rows))
	}
}

func TestCreateAgentFailureLeavesErrorRow(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{
		createFn: func(_ context.Context, in *hv1.CreateWorkspaceRequest) (*hv1.Workspace, error) {
			return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_ERROR, Message: "boom"}, nil
		},
	}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if !errors.Is(err, ErrAgentCall) {
		t.Fatalf("err = %v, want ErrAgentCall", err)
	}
	if ws.State != "error" {
		t.Fatalf("returned state = %q, want error", ws.State)
	}
	stored, ok := st.get(ws.ID)
	if !ok || stored.State != "error" {
		t.Fatalf("stored row = %+v ok=%v, want state error", stored, ok)
	}
	if !contains(st.eventKinds(), "error") {
		t.Fatalf("events = %v, want an error event", st.eventKinds())
	}
}

func TestCreateRejectsEmptyName(t *testing.T) {
	st := newFakeStore()
	s := newTestService(t, st, readyRegistry(), &fakeDialer{client: &fakeAgentClient{}})

	_, err := s.Create(context.Background(), mustUUID(t, ownerAID), "   ", "")
	if !errors.Is(err, ErrNoName) {
		t.Fatalf("err = %v, want ErrNoName", err)
	}
	if len(st.rows) != 0 {
		t.Fatalf("expected no row, got %d", len(st.rows))
	}
}

func TestGetIsOwnerScoped(t *testing.T) {
	st := newFakeStore()
	s := newTestService(t, st, readyRegistry(), &fakeDialer{client: &fakeAgentClient{}})

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Get(context.Background(), mustUUID(t, ownerAID), ws.ID); err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if _, err := s.Get(context.Background(), mustUUID(t, ownerBID), ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Get err = %v, want ErrNotFound", err)
	}
}

func TestStopUpdatesStateFromAgent(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Stop(context.Background(), mustUUID(t, ownerAID), ws.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got.State != "stopped" {
		t.Fatalf("returned state = %q, want stopped", got.State)
	}
	stored, _ := st.get(ws.ID)
	if stored.State != "stopped" {
		t.Fatalf("stored state = %q, want stopped", stored.State)
	}
	if !contains(st.eventKinds(), "stopped") {
		t.Fatalf("events = %v, want stopped", st.eventKinds())
	}
}

func TestStopAgentErrorStateReturnsErrAgentCall(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{
		stopFn: func(_ context.Context, in *hv1.WorkspaceRef) (*hv1.Workspace, error) {
			return &hv1.Workspace{WorkspaceId: in.WorkspaceId, State: hv1.WorkspaceState_ERROR, Message: "kaput"}, nil
		},
	}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Stop(context.Background(), mustUUID(t, ownerAID), ws.ID); !errors.Is(err, ErrAgentCall) {
		t.Fatalf("Stop err = %v, want ErrAgentCall", err)
	}
	stored, _ := st.get(ws.ID)
	if stored.State != "error" {
		t.Fatalf("stored state = %q, want error", stored.State)
	}
}

func TestDestroyRemovesRowOnAgentSuccess(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Destroy(context.Background(), mustUUID(t, ownerAID), ws.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := st.get(ws.ID); ok {
		t.Fatalf("row still present after Destroy")
	}
	if !contains(st.eventKinds(), "deleting") {
		t.Fatalf("events = %v, want deleting", st.eventKinds())
	}
}

func TestDestroyAgentFailureLeavesErrorRow(t *testing.T) {
	st := newFakeStore()
	dial := &fakeDialer{client: &fakeAgentClient{
		destroyFn: func(context.Context, *hv1.WorkspaceRef) (*hv1.DestroyResponse, error) {
			return nil, errors.New("rpc down")
		},
	}}
	s := newTestService(t, st, readyRegistry(), dial)

	ws, err := s.Create(context.Background(), mustUUID(t, ownerAID), "dev", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Destroy(context.Background(), mustUUID(t, ownerAID), ws.ID); !errors.Is(err, ErrAgentCall) {
		t.Fatalf("Destroy err = %v, want ErrAgentCall", err)
	}
	stored, ok := st.get(ws.ID)
	if !ok {
		t.Fatalf("row was deleted despite agent failure")
	}
	if stored.State != "error" {
		t.Fatalf("stored state = %q, want error", stored.State)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
