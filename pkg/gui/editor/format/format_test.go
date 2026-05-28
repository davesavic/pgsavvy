package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat_SimpleSelect(t *testing.T) {
	got, err := Format("select id, name from users where id = 1")
	require.NoError(t, err)
	assert.Contains(t, got, "SELECT")
	assert.Contains(t, got, "FROM")
	assert.Contains(t, got, "WHERE")
}

func TestFormat_InvalidSQL(t *testing.T) {
	_, err := Format("NOT VALID SQL !!! {{{}}")
	assert.Error(t, err)
}

func TestFormat_MultiStatement(t *testing.T) {
	input := "select 1; select 2"
	got, err := Format(input)
	require.NoError(t, err)
	// Both statements should appear, each terminated with a semicolon.
	assert.Contains(t, got, "SELECT 1;")
	assert.Contains(t, got, "SELECT 2;")
}

func TestFormat_EmptyInput(t *testing.T) {
	got, err := Format("")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFormat_WhitespaceOnly(t *testing.T) {
	got, err := Format("   \n\t  ")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFormat_SelectWithJoin(t *testing.T) {
	input := "select u.id, o.total from users u inner join orders o on u.id = o.user_id where o.total > 100"
	got, err := Format(input)
	require.NoError(t, err)
	assert.Contains(t, got, "SELECT")
	assert.Contains(t, got, "JOIN")
}
