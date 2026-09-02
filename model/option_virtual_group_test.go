package model

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionMapRejectsInvalidModelGroupsWithoutChangingRuntime(t *testing.T) {
	previousGroups := make(map[string][]string)
	for _, name := range VirtualGroupNames() {
		previousGroups[name] = VirtualGroupConfigOrder(name)
	}
	previousGroupsJSON, err := json.Marshal(previousGroups)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, LoadVirtualModelGroups(string(previousGroupsJSON)))
	})

	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previous
		common.OptionMapRWMutex.Unlock()
	})

	require.NoError(t, updateOptionMap("ModelGroups", `{"stable":["model-a"]}`))
	beforeVersion := VirtualGroupVersion()

	err = updateOptionMap("ModelGroups", `{"broken":`)
	require.Error(t, err)
	assert.Equal(t, beforeVersion, VirtualGroupVersion())
	assert.Equal(t, []string{"model-a"}, VirtualGroupConfigOrder("stable"))
	assert.False(t, IsVirtualModel("broken"))

	common.OptionMapRWMutex.RLock()
	stored := common.OptionMap["ModelGroups"]
	common.OptionMapRWMutex.RUnlock()
	assert.Equal(t, `{"stable":["model-a"]}`, stored)
}
