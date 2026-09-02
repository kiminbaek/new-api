package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

func TestIs64BitIntegerType(t *testing.T) {
	tests := []struct {
		name     string
		dbType   common.DatabaseType
		dataType string
		want     bool
	}{
		{"mysql bigint", common.DatabaseTypeMySQL, "bigint", true},
		{"mysql unsigned bigint", common.DatabaseTypeMySQL, "bigint unsigned", true},
		{"mysql int rejected", common.DatabaseTypeMySQL, "int", false},
		{"postgres bigint", common.DatabaseTypePostgreSQL, "bigint", true},
		{"postgres int8", common.DatabaseTypePostgreSQL, "int8", true},
		{"postgres integer rejected", common.DatabaseTypePostgreSQL, "integer", false},
		{"unknown rejected", common.DatabaseType(""), "bigint", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, is64BitIntegerType(tt.dbType, tt.dataType))
		})
	}
}
