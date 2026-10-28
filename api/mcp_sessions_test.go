package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Any Coroot replica accepts the session ids any other made (they share the
// auth secret), and no id it did not sign.
func TestMcpSessionIdsAreAcceptedByEveryReplica(t *testing.T) {
	secret := func() string { return "the auth secret of the database" }
	one, other := &mcpSessionIdManager{secret: secret}, &mcpSessionIdManager{secret: secret}

	id := one.Generate()
	terminated, err := other.Validate(id)
	assert.NoError(t, err, "made by another replica")
	assert.False(t, terminated)
	assert.NotEqual(t, id, one.Generate(), "every session its own id")

	for _, invalid := range []string{"", "no-signature", id + "x", "x" + id, strings.Replace(id, ".", ".x", 1)} {
		_, err = other.Validate(invalid)
		assert.Error(t, err, "%q", invalid)
	}
	stranger := &mcpSessionIdManager{secret: func() string { return "another Coroot" }}
	_, err = stranger.Validate(id)
	assert.Error(t, err, "not signed with its secret")
}
