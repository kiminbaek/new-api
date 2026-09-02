package xunfei

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// https://console.xfyun.cn/services/cbm
// https://www.xfyun.cn/doc/spark/Web.html

func requestOpenAI2Xunfei(request dto.GeneralOpenAIRequest, xunfeiAppId string, domain string) *XunfeiChatRequest {
	messages := make([]XunfeiMessage, 0, len(request.Messages))
	shouldCovertSystemMessage := !strings.HasSuffix(request.Model, "3.5")
	for _, message := range request.Messages {
		if message.Role == "system" && shouldCovertSystemMessage {
			messages = append(messages, XunfeiMessage{
				Role:    "user",
				Content: message.StringContent(),
			})
			messages = append(messages, XunfeiMessage{
				Role:    "assistant",
				Content: "Okay",
			})
		} else {
			messages = append(messages, XunfeiMessage{
				Role:    message.Role,
				Content: message.StringContent(),
			})
		}
	}
	xunfeiRequest := XunfeiChatRequest{}
	xunfeiRequest.Header.AppId = xunfeiAppId
	xunfeiRequest.Parameter.Chat.Domain = domain
	xunfeiRequest.Parameter.Chat.Temperature = request.Temperature
	xunfeiRequest.Parameter.Chat.TopK = lo.FromPtrOr(request.N, 0)
	xunfeiRequest.Parameter.Chat.MaxTokens = request.GetMaxTokens()
	xunfeiRequest.Payload.Message.Text = messages
	return &xunfeiRequest
}

func responseXunfei2OpenAI(response *XunfeiChatResponse) *dto.OpenAITextResponse {
	if len(response.Payload.Choices.Text) == 0 {
		response.Payload.Choices.Text = []XunfeiChatResponseTextItem{
			{
				Content: "",
			},
		}
	}
	choice := dto.OpenAITextResponseChoice{
		Index: 0,
		Message: dto.Message{
			Role:    "assistant",
			Content: response.Payload.Choices.Text[0].Content,
		},
		FinishReason: constant.FinishReasonStop,
	}
	fullTextResponse := dto.OpenAITextResponse{
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Choices: []dto.OpenAITextResponseChoice{choice},
		Usage:   response.Payload.Usage.Text,
	}
	return &fullTextResponse
}

func streamResponseXunfei2OpenAI(xunfeiResponse *XunfeiChatResponse) *dto.ChatCompletionsStreamResponse {
	if len(xunfeiResponse.Payload.Choices.Text) == 0 {
		xunfeiResponse.Payload.Choices.Text = []XunfeiChatResponseTextItem{
			{
				Content: "",
			},
		}
	}
	var choice dto.ChatCompletionsStreamResponseChoice
	choice.Delta.SetContentString(xunfeiResponse.Payload.Choices.Text[0].Content)
	if xunfeiResponse.Payload.Choices.Status == 2 {
		choice.FinishReason = &constant.FinishReasonStop
	}
	response := dto.ChatCompletionsStreamResponse{
		Object:  "chat.completion.chunk",
		Created: common.GetTimestamp(),
		Model:   "SparkDesk",
		Choices: []dto.ChatCompletionsStreamResponseChoice{choice},
	}
	return &response
}

func buildXunfeiAuthUrl(hostUrl string, apiKey, apiSecret string) string {
	HmacWithShaToBase64 := func(algorithm, data, key string) string {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(data))
		encodeData := mac.Sum(nil)
		return base64.StdEncoding.EncodeToString(encodeData)
	}
	ul, err := url.Parse(hostUrl)
	if err != nil {
		fmt.Println(err)
	}
	date := time.Now().UTC().Format(time.RFC1123)
	signString := []string{"host: " + ul.Host, "date: " + date, "GET " + ul.Path + " HTTP/1.1"}
	sign := strings.Join(signString, "\n")
	sha := HmacWithShaToBase64("hmac-sha256", sign, apiSecret)
	authUrl := fmt.Sprintf("hmac username=\"%s\", algorithm=\"%s\", headers=\"%s\", signature=\"%s\"", apiKey,
		"hmac-sha256", "host date request-line", sha)
	authorization := base64.StdEncoding.EncodeToString([]byte(authUrl))
	v := url.Values{}
	v.Add("host", ul.Host)
	v.Add("date", date)
	v.Add("authorization", authorization)
	callUrl := hostUrl + "?" + v.Encode()
	return callUrl
}

type xunfeiStreamEvent struct {
	Response *XunfeiChatResponse
	Err      error
}

var xunfeiRequest = xunfeiMakeRequest

func xunfeiStreamHandler(c *gin.Context, textRequest dto.GeneralOpenAIRequest, appId string, apiSecret string, apiKey string) (*dto.Usage, *types.NewAPIError) {
	domain, authUrl := getXunfeiAuthUrl(c, apiKey, apiSecret, textRequest.Model)
	events, err := xunfeiRequest(c.Request.Context(), textRequest, domain, authUrl, appId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	helper.SetEventStreamHeaders(c)
	var usage dto.Usage
	validFrames := 0
	terminated := false
	for event := range events {
		if event.Err != nil {
			if c.Request.Context().Err() != nil {
				return &usage, nil
			}
			return nil, types.NewOpenAIError(event.Err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		response := event.Response
		if response == nil {
			continue
		}
		if response.Header.Code != 0 {
			return nil, types.NewOpenAIError(fmt.Errorf("xunfei error %d: %s", response.Header.Code, response.Header.Message), types.ErrorCodeBadResponse, http.StatusBadGateway)
		}
		usage.PromptTokens += response.Payload.Usage.Text.PromptTokens
		usage.CompletionTokens += response.Payload.Usage.Text.CompletionTokens
		usage.TotalTokens += response.Payload.Usage.Text.TotalTokens
		if len(response.Payload.Choices.Text) > 0 {
			validFrames++
			openAIResponse := streamResponseXunfei2OpenAI(response)
			jsonResponse, marshalErr := json.Marshal(openAIResponse)
			if marshalErr != nil {
				return nil, types.NewError(marshalErr, types.ErrorCodeBadResponseBody)
			}
			c.Render(-1, common.CustomEvent{Data: "data: " + string(jsonResponse)})
		}
		if response.Payload.Choices.Status == 2 {
			terminated = true
		}
	}
	if validFrames == 0 || !terminated {
		return nil, types.NewOpenAIError(fmt.Errorf("xunfei stream ended without valid output (frames=%d terminated=%t)", validFrames, terminated), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}
	c.Render(-1, common.CustomEvent{Data: "data: [DONE]"})
	return &usage, nil
}

func xunfeiHandler(c *gin.Context, textRequest dto.GeneralOpenAIRequest, appId string, apiSecret string, apiKey string) (*dto.Usage, *types.NewAPIError) {
	domain, authUrl := getXunfeiAuthUrl(c, apiKey, apiSecret, textRequest.Model)
	events, err := xunfeiRequest(c.Request.Context(), textRequest, domain, authUrl, appId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	var usage dto.Usage
	var content string
	var last *XunfeiChatResponse
	terminated := false
	for event := range events {
		if event.Err != nil {
			return nil, types.NewOpenAIError(event.Err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		response := event.Response
		if response == nil {
			continue
		}
		if response.Header.Code != 0 {
			return nil, types.NewOpenAIError(fmt.Errorf("xunfei error %d: %s", response.Header.Code, response.Header.Message), types.ErrorCodeBadResponse, http.StatusBadGateway)
		}
		last = response
		for _, text := range response.Payload.Choices.Text {
			content += text.Content
		}
		usage.PromptTokens += response.Payload.Usage.Text.PromptTokens
		usage.CompletionTokens += response.Payload.Usage.Text.CompletionTokens
		usage.TotalTokens += response.Payload.Usage.Text.TotalTokens
		if response.Payload.Choices.Status == 2 {
			terminated = true
		}
	}
	if last == nil || content == "" || !terminated {
		return nil, types.NewOpenAIError(fmt.Errorf("xunfei response ended without valid output"), types.ErrorCodeBadResponse, http.StatusBadGateway)
	}
	last.Payload.Choices.Text = []XunfeiChatResponseTextItem{{Content: content}}
	response := responseXunfei2OpenAI(last)
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	_, _ = c.Writer.Write(jsonResponse)
	return &usage, nil
}

func xunfeiMakeRequest(ctx context.Context, textRequest dto.GeneralOpenAIRequest, domain, authUrl, appId string) (<-chan xunfeiStreamEvent, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, authUrl, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, fmt.Errorf("xunfei websocket handshake failed: status=%d", status)
	}
	if err := conn.WriteJSON(requestOpenAI2Xunfei(textRequest, appId, domain)); err != nil {
		_ = conn.Close()
		return nil, err
	}

	events := make(chan xunfeiStreamEvent, 1)
	go func() {
		defer close(events)
		defer conn.Close()
		go func() { <-ctx.Done(); _ = conn.Close() }()
		for {
			_, msg, readErr := conn.ReadMessage()
			if readErr != nil {
				if ctx.Err() == nil {
					events <- xunfeiStreamEvent{Err: readErr}
				}
				return
			}
			var response XunfeiChatResponse
			if err := json.Unmarshal(msg, &response); err != nil {
				events <- xunfeiStreamEvent{Err: err}
				return
			}
			select {
			case events <- xunfeiStreamEvent{Response: &response}:
			case <-ctx.Done():
				return
			}
			if response.Payload.Choices.Status == 2 {
				return
			}
		}
	}()
	return events, nil
}

func apiVersion2domain(apiVersion string) string {
	switch apiVersion {
	case "v1.1":
		return "lite"
	case "v2.1":
		return "generalv2"
	case "v3.1":
		return "generalv3"
	case "v3.5":
		return "generalv3.5"
	case "v4.0":
		return "4.0Ultra"
	}
	return "general" + apiVersion
}

func getXunfeiAuthUrl(c *gin.Context, apiKey string, apiSecret string, modelName string) (string, string) {
	apiVersion := getAPIVersion(c, modelName)
	domain := apiVersion2domain(apiVersion)
	authUrl := buildXunfeiAuthUrl(fmt.Sprintf("wss://spark-api.xf-yun.com/%s/chat", apiVersion), apiKey, apiSecret)
	return domain, authUrl
}

func getAPIVersion(c *gin.Context, modelName string) string {
	query := c.Request.URL.Query()
	apiVersion := query.Get("api-version")
	if apiVersion != "" {
		return apiVersion
	}
	parts := strings.Split(modelName, "-")
	if len(parts) == 2 {
		apiVersion = parts[1]
		return apiVersion

	}
	apiVersion = c.GetString("api_version")
	if apiVersion != "" {
		return apiVersion
	}
	apiVersion = "v1.1"
	common.SysLog("api_version not found, using default: " + apiVersion)
	return apiVersion
}
