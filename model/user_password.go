package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type userPasswordState struct {
	Password string
	Status   int
}

func loadUserPasswordState(userID int) (userPasswordState, error) {
	var state userPasswordState
	err := DB.Model(&User{}).Select("password", "status").Where("id = ?", userID).Take(&state).Error
	return state, err
}

// HasUserPassword exposes only whether a local password exists. The password
// hash remains inside the model layer and is never returned by dashboard DTOs.
func HasUserPassword(userID int) (bool, error) {
	state, err := loadUserPasswordState(userID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(state.Password) != "", nil
}

// VerifyUserPassword validates a current local password without exposing its
// hash. Disabled users never receive a reusable security proof.
func VerifyUserPassword(userID int, password string) (bool, error) {
	if strings.TrimSpace(password) == "" {
		return false, nil
	}
	state, err := loadUserPasswordState(userID)
	if err != nil {
		return false, err
	}
	if state.Status != common.UserStatusEnabled || state.Password == "" {
		return false, nil
	}
	return common.ValidatePasswordAndHash(password, state.Password), nil
}
