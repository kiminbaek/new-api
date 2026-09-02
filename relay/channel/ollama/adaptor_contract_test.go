package ollama

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestOllamaRequestURLContracts(t *testing.T) {
	a := &Adaptor{}
	base := &relaycommon.ChannelMeta{ChannelBaseUrl: "http://ollama:11434"}
	tests := []struct {
		name string
		info *relaycommon.RelayInfo
		want string
	}{
		{"chat", &relaycommon.RelayInfo{ChannelMeta: base}, "http://ollama:11434/api/chat"},
		{"completion", &relaycommon.RelayInfo{ChannelMeta: base, RelayMode: relayconstant.RelayModeCompletions}, "http://ollama:11434/api/generate"},
		{"embedding", &relaycommon.RelayInfo{ChannelMeta: base, RelayMode: relayconstant.RelayModeEmbeddings}, "http://ollama:11434/api/embed"},
		{"responses", &relaycommon.RelayInfo{ChannelMeta: base, RelayMode: relayconstant.RelayModeResponses}, "http://ollama:11434/v1/responses"},
		{"responses compact", &relaycommon.RelayInfo{ChannelMeta: base, RelayMode: relayconstant.RelayModeResponsesCompact}, "http://ollama:11434/v1/responses/compact"},
		{"claude", &relaycommon.RelayInfo{ChannelMeta: base, RelayFormat: types.RelayFormatClaude}, "http://ollama:11434/v1/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.GetRequestURL(tt.info)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
