package common

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPasswordEncryptionRoundTripAndUniformErrors(t *testing.T) {
	pemKey, err := GeneratePasswordEncryptionPrivateKey()
	require.NoError(t, err)
	require.NoError(t, LoadPasswordEncryptionPrivateKey(pemKey))
	kid, publicPEM := PasswordEncryptionPublicKey()
	require.NotEmpty(t, kid)
	require.Contains(t, publicPEM, "BEGIN PUBLIC KEY")

	password := "sensitive-password-密码"
	passwordEncryptionState.RLock()
	publicKey := passwordEncryptionState.privateKey.PublicKey
	passwordEncryptionState.RUnlock()
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &publicKey, []byte(password), nil)
	require.NoError(t, err)
	plain, err := DecryptPassword(base64.StdEncoding.EncodeToString(ciphertext), kid)
	require.NoError(t, err)
	require.Equal(t, password, plain)

	for _, input := range []struct{ ciphertext, keyID string }{
		{"not-base64", kid},
		{base64.StdEncoding.EncodeToString(ciphertext), "wrong-key"},
		{"", kid},
	} {
		_, err := DecryptPassword(input.ciphertext, input.keyID)
		require.ErrorIs(t, err, ErrPasswordEncryptionInvalid)
	}
}
