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

func TestShowEmulatorHandleSetCommand_SetNamesWhitespaceVariants(t *testing.T) {
	se := NewShowEmulator()

	testCases := []struct {
		name              string
		sql               string
		expectedCharset   string
		expectedCollation string
	}{
		{
			name:            "leading space",
			sql:             " SET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:            "leading crlf",
			sql:             "\r\nSET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:            "leading tab",
			sql:             "\tSET NAMES utf8mb4",
			expectedCharset: "utf8mb4",
		},
		{
			name:              "with collate",
			sql:               "  SET NAMES utf8mb4 COLLATE utf8mb4_general_ci",
			expectedCharset:   "utf8mb4",
			expectedCollation: "utf8mb4_general_ci",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sessionVars := make(map[string]interface{})

			err := se.HandleSetCommand(context.Background(), tc.sql, sessionVars)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedCharset, sessionVars["names"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_client"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_connection"])
			assert.Equal(t, tc.expectedCharset, sessionVars["character_set_results"])

			if tc.expectedCollation != "" {
				assert.Equal(t, tc.expectedCollation, sessionVars["collation_connection"])
			}
		})
	}
}

func TestShowEmulatorHandleSetCommand_AssignmentSyntax(t *testing.T) {
	se := NewShowEmulator()
	sessionVars := make(map[string]interface{})

	err := se.HandleSetCommand(context.Background(), "SET autocommit = 1", sessionVars)
	require.NoError(t, err)
	assert.Equal(t, "1", sessionVars["autocommit"])
}
