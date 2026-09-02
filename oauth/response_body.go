package oauth

import (
	"fmt"
	"io"
)

const maxOAuthResponseBytes = 1 << 20

func readOAuthResponseBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxOAuthResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxOAuthResponseBytes {
		return nil, fmt.Errorf("oauth response exceeds %d bytes", maxOAuthResponseBytes)
	}
	return body, nil
}
