package store

import "github.com/jackc/pgx/v5/pgtype"

// UUIDString renders a pgtype.UUID as canonical hyphenated text, or "" if unset.
func UUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	s, _ := u.Value() // pgtype.UUID.Value never errors for a Valid uuid
	return s.(string)
}

// ParseUUID parses canonical UUID text into a pgtype.UUID.
func ParseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	err := u.Scan(s)
	return u, err
}
