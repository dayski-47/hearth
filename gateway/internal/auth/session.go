package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/dayski-47/hearth/gateway/internal/store/gen"
	"github.com/jackc/pgx/v5/pgtype"
)

// sessionTTL is the rolling lifetime of a session. Every authenticated request
// slides it forward, so an active user is never logged out and an idle one is
// after this long.
const sessionTTL = 7 * 24 * time.Hour

// ErrNoSession is returned by Authenticate for every failure mode: no cookie,
// bad signature, unknown id, or expired. The caller only needs "not logged in".
var ErrNoSession = errors.New("auth: no valid session")

// Queries is the slice of the generated store the manager needs. *gen.Queries
// satisfies it; tests pass a fake.
type Queries interface {
	CreateSession(context.Context, gen.CreateSessionParams) (gen.Session, error)
	GetSessionWithUser(context.Context, string) (gen.GetSessionWithUserRow, error)
	SlideSession(context.Context, gen.SlideSessionParams) error
	DeleteSession(context.Context, string) error
}

type Manager struct {
	q      Queries
	secret []byte
	logger *slog.Logger
	now    func() time.Time
}

func NewManager(q Queries, secret []byte, logger *slog.Logger) *Manager {
	return &Manager{q: q, secret: secret, logger: logger, now: time.Now}
}

func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create writes a new session row and returns the signed cookie value.
func (m *Manager) Create(ctx context.Context, userID pgtype.UUID, userAgent string, ip netip.Addr) (string, error) {
	id, err := newSessionID()
	if err != nil {
		return "", err
	}
	var ipp *netip.Addr
	if ip.IsValid() {
		ipp = &ip
	}
	if _, err := m.q.CreateSession(ctx, gen.CreateSessionParams{
		ID:        id,
		UserID:    userID,
		ExpiresAt: pgtype.Timestamptz{Time: m.now().Add(sessionTTL), Valid: true},
		UserAgent: userAgent,
		Ip:        ipp,
	}); err != nil {
		return "", err
	}
	return signValue(m.secret, id), nil
}

// Authenticate verifies the cookie, loads the session and user, checks expiry,
// and slides the renewal.
func (m *Manager) Authenticate(ctx context.Context, cookieValue string) (*gen.User, error) {
	id, ok := parseValue(m.secret, cookieValue)
	if !ok {
		return nil, ErrNoSession
	}
	row, err := m.q.GetSessionWithUser(ctx, id)
	if err != nil {
		return nil, ErrNoSession
	}
	if !row.Session.ExpiresAt.Valid || m.now().After(row.Session.ExpiresAt.Time) {
		if err := m.q.DeleteSession(ctx, id); err != nil {
			m.logger.WarnContext(ctx, "delete expired session failed", "error", err)
		}
		return nil, ErrNoSession
	}
	if err := m.q.SlideSession(ctx, gen.SlideSessionParams{
		ID:        id,
		ExpiresAt: pgtype.Timestamptz{Time: m.now().Add(sessionTTL), Valid: true},
	}); err != nil {
		m.logger.WarnContext(ctx, "slide session failed", "error", err)
	}
	u := row.User
	return &u, nil
}

// Destroy deletes the session behind the cookie value, if the value parses.
func (m *Manager) Destroy(ctx context.Context, cookieValue string) {
	id, ok := parseValue(m.secret, cookieValue)
	if !ok {
		return
	}
	if err := m.q.DeleteSession(ctx, id); err != nil {
		m.logger.WarnContext(ctx, "destroy session failed", "error", err)
	}
}
