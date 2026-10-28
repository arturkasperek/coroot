package api

import (
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/utils"
)

// mcpSessionIdManager makes the ids of MCP sessions that any Coroot replica
// accepts: an id is a random value signed with the auth secret, which every
// replica reads from the database, so it is checked without a list of
// sessions in memory. What a session keeps (the selected project) is in the
// database too.
type mcpSessionIdManager struct {
	secret func() string
	db     *db.DB
}

func (m *mcpSessionIdManager) sign(value string) string {
	h := hmac.New(HashFunc.New, []byte(m.secret()))
	h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (m *mcpSessionIdManager) Generate() string {
	value := utils.RandomString(24)
	return value + "." + m.sign(value)
}

func (m *mcpSessionIdManager) Validate(sessionId string) (bool, error) {
	value, signature, ok := strings.Cut(sessionId, ".")
	if !ok || value == "" || !hmac.Equal([]byte(signature), []byte(m.sign(value))) {
		return false, errors.New("invalid session id")
	}
	return false, nil
}

// Terminate forgets the project of a session the client ended; the id stays
// valid, as a signature cannot be taken back, but selects nothing any more.
func (m *mcpSessionIdManager) Terminate(sessionId string) (bool, error) {
	if _, err := m.Validate(sessionId); err != nil {
		return false, err
	}
	return false, m.db.DeleteMcpSession(sessionId)
}
