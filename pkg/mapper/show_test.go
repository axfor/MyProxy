package mapper

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShowEmulatorHandleSetCommand_SetNames(t *testing.T) {
	se := NewShowEmulator()
	sessionVars := make(map[string]interface{})

	err := se.HandleSetCommand(context.Background(), "SET NAMES utf8mb4", sessionVars)
	require.NoError(t, err)
	assert.Equal(t, "utf8mb4", sessionVars["names"])
	assert.Equal(t, "utf8mb4", sessionVars["character_set_client"])
	assert.Equal(t, "utf8mb4", sessionVars["character_set_connection"])
	assert.Equal(t, "utf8mb4", sessionVars["character_set_results"])
}

func TestShowEmulatorHandleSetCommand_AssignmentSyntax(t *testing.T) {
	se := NewShowEmulator()
	sessionVars := make(map[string]interface{})

	err := se.HandleSetCommand(context.Background(), "SET autocommit = 1", sessionVars)
	require.NoError(t, err)
	assert.Equal(t, "1", sessionVars["autocommit"])
}
