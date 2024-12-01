package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"one-api/common"
	"one-api/constant"
	"one-api/dto"
	relaycommon "one-api/relay/common"
	relayconstant "one-api/relay/constant"
	"one-api/service"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func OaiStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	containStreamUsage := false
	var responseId string
	var createAt int64 = 0
	var systemFingerprint string
	model := info.UpstreamModelName

	var responseTextBuilder strings.Builder
	var usage = &dto.Usage{}
	var streamItems []string // store stream items

	toolCount := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(bufio.ScanLines)

	service.SetEventStreamHeaders(c)

	ticker := time.NewTicker(time.Duration(constant.StreamingTimeout) * time.Second)
	defer ticker.Stop()

	stopChan := make(chan bool)
	defer close(stopChan)
	var (
		lastStreamData string
		mu             sync.Mutex
	)
	gopool.Go(func() {
		for scanner.Scan() {
			info.SetFirstResponseTime()
			ticker.Reset(time.Duration(constant.StreamingTimeout) * time.Second)
			data := scanner.Text()
			if len(data) < 6 { // ignore blank line or wrong format
				continue
			}
			if data[:6] != "data: " && data[:6] != "[DONE]" {
				continue
			}
			mu.Lock()
			data = data[6:]
			if !strings.HasPrefix(data, "[DONE]") && !strings.Contains(data, "\",\"created\":1234567890,\"model\":\"\",\"") {
				if lastStreamData != "" {
					err := service.StringData(c, lastStreamData)
					if err != nil {
						common.LogError(c, "streaming error: "+err.Error())
					}
				}
				lastStreamData = data
				streamItems = append(streamItems, data)
			}
			mu.Unlock()
		}
		common.SafeSendBool(stopChan, true)
	})

	select {
	case <-ticker.C:
		// 超时处理逻辑
		common.LogError(c, "streaming timeout")
	case <-stopChan:
		// 正常结束
	}

	shouldSendLastResp := true
	var lastStreamResponse dto.ChatCompletionsStreamResponse
	err := json.Unmarshal(common.StringToByteSlice(lastStreamData), &lastStreamResponse)
	if err == nil {
		responseId = lastStreamResponse.Id
		createAt = lastStreamResponse.Created
		systemFingerprint = lastStreamResponse.GetSystemFingerprint()
		model = lastStreamResponse.Model
		if service.ValidUsage(lastStreamResponse.Usage) {
			containStreamUsage = true
			usage = lastStreamResponse.Usage
			if !info.ShouldIncludeUsage {
				shouldSendLastResp = false
			}
		}
		for _, choice := range lastStreamResponse.Choices {
			if choice.FinishReason != nil {
				shouldSendLastResp = true
			}
		}
	}
	if shouldSendLastResp {
		service.StringData(c, lastStreamData)
	}

	// 计算token
	streamResp := "[" + strings.Join(streamItems, ",") + "]"
	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions:
		var streamResponses []dto.ChatCompletionsStreamResponse
		err := json.Unmarshal(common.StringToByteSlice(streamResp), &streamResponses)
		if err != nil {
			// 一次性解析失败，逐个解析
			common.SysError("error unmarshalling stream response: " + err.Error())
			for _, item := range streamItems {
				var streamResponse dto.ChatCompletionsStreamResponse
				err := json.Unmarshal(common.StringToByteSlice(item), &streamResponse)
				if err == nil {
					//if service.ValidUsage(streamResponse.Usage) {
					//	usage = streamResponse.Usage
					//}
					for _, choice := range streamResponse.Choices {
						responseTextBuilder.WriteString(choice.Delta.GetContentString())
						if choice.Delta.ToolCalls != nil {
							if len(choice.Delta.ToolCalls) > toolCount {
								toolCount = len(choice.Delta.ToolCalls)
							}
							for _, tool := range choice.Delta.ToolCalls {
								responseTextBuilder.WriteString(tool.Function.Name)
								responseTextBuilder.WriteString(tool.Function.Arguments)
							}
						}
					}
				}
			}
		} else {
			for _, streamResponse := range streamResponses {
				//if service.ValidUsage(streamResponse.Usage) {
				//	usage = streamResponse.Usage
				//	containStreamUsage = true
				//}
				for _, choice := range streamResponse.Choices {
					responseTextBuilder.WriteString(choice.Delta.GetContentString())
					if choice.Delta.ToolCalls != nil {
						if len(choice.Delta.ToolCalls) > toolCount {
							toolCount = len(choice.Delta.ToolCalls)
						}
						for _, tool := range choice.Delta.ToolCalls {
							responseTextBuilder.WriteString(tool.Function.Name)
							responseTextBuilder.WriteString(tool.Function.Arguments)
						}
					}
				}
			}
		}
	case relayconstant.RelayModeCompletions:
		var streamResponses []dto.CompletionsStreamResponse
		err := json.Unmarshal(common.StringToByteSlice(streamResp), &streamResponses)
		if err != nil {
			// 一次性解析失败，逐个解析
			common.SysError("error unmarshalling stream response: " + err.Error())
			for _, item := range streamItems {
				var streamResponse dto.CompletionsStreamResponse
				err := json.Unmarshal(common.StringToByteSlice(item), &streamResponse)
				if err == nil {
					for _, choice := range streamResponse.Choices {
						responseTextBuilder.WriteString(choice.Text)
					}
				}
			}
		} else {
			for _, streamResponse := range streamResponses {
				for _, choice := range streamResponse.Choices {
					responseTextBuilder.WriteString(choice.Text)
				}
			}
		}
	}

	if !containStreamUsage {
		usage, _ = service.ResponseText2Usage(responseTextBuilder.String(), info.UpstreamModelName, info.PromptTokens)
		usage.CompletionTokens += toolCount * 7
	}

	if info.ShouldIncludeUsage && !containStreamUsage {
		response := service.GenerateFinalUsageResponse(responseId, createAt, model, *usage)
		response.SetSystemFingerprint(systemFingerprint)
		service.ObjectData(c, response)
	}

	service.Done(c)

	resp.Body.Close()
	return nil, usage
}

func OpenaiHandler(c *gin.Context, resp *http.Response, promptTokens int, model string) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	// 读取响应体
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}

	// 重置响应体
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))

	// 检查是否有错误信息
	var openAIError dto.OpenAIError
	err = json.Unmarshal(responseBody, &openAIError)
	if err == nil && openAIError.Type != "" {
		return &dto.OpenAIErrorWithStatusCode{
			Error:      openAIError,
			StatusCode: resp.StatusCode,
		}, nil
	}

	// 从上下文中获取 response_mapping 字符串
	responseMappingStr := c.GetString("response_mapping")
	// 初始化一个 mapping 用于存储替换规则
	var responseMapping map[string]string
	// 检查 responseMappingStr 是否为空
	if strings.TrimSpace(responseMappingStr) != "" {
		// 尝试解析 JSON 字符串
		err := json.Unmarshal([]byte(responseMappingStr), &responseMapping)
		if err != nil {
			// 如果解析失败，不进行替换操作，避免程序崩溃
			responseMapping = nil
		}
	}

	// 标记是否进行了替换操作
	bodyModified := false

	// 如果有有效的替换规则，进行内容替换
	if responseMapping != nil {
		// 使用通用的 map 来解析响应，避免信息丢失
		var responseMap map[string]interface{}
		err := json.Unmarshal(responseBody, &responseMap)
		if err == nil {
			// 处理 choices 中的内容
			if choices, ok := responseMap["choices"].([]interface{}); ok {
				for _, choice := range choices {
					if choiceMap, ok := choice.(map[string]interface{}); ok {
						// 处理 message.content
						if message, ok := choiceMap["message"].(map[string]interface{}); ok {
							if content, ok := message["content"].(string); ok {
								originalContent := content
								// 遍历替换规则，进行逐一替换
								for pattern, replacement := range responseMapping {
									// 尝试从缓存中获取已编译的正则表达式
									var re *regexp.Regexp
									if cachedRe, ok := common.RegexCache.Load(pattern); ok {
										re = cachedRe.(*regexp.Regexp)
									} else {
										// 编译新的正则表达式并存入缓存
										var err error
										re, err = regexp.Compile(pattern)
										if err != nil {
											// 编译失败，跳过该规则
											continue
										}
										common.RegexCache.Store(pattern, re)
									}
									content = re.ReplaceAllString(content, replacement)
								}
								// 如果内容发生了变化，标记为已修改
								if content != originalContent {
									bodyModified = true
								}
								// 更新替换后的内容
								message["content"] = content
							}
						}
					}
				}
			}
			// 在重新序列化响应内容后，添加换行符
			filteredResponseBody, err := json.Marshal(responseMap)
			if err == nil {
				// 添加换行符
				filteredResponseBody = append(filteredResponseBody, '\n')
				// 重置 response body
				resp.Body = io.NopCloser(bytes.NewBuffer(filteredResponseBody))
				// 如果内容被修改，更新 responseBody 变量
				if bodyModified {
					responseBody = filteredResponseBody
				}
			} else {
				// 如果序列化失败，保持原始响应内容
				resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
			}
		} else {
			// 如果解析失败，保持原始响应内容
			resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
		}
	} else {
		// 如果没有有效的替换规则，保持原始响应内容
		resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	}

	// 如果响应体被修改，更新 Content-Length 头部
	if bodyModified {
		// 获取新的内容长度
		newContentLength := strconv.Itoa(len(responseBody))
		// 更新响应头中的 Content-Length
		resp.Header.Set("Content-Length", newContentLength)
	}

	// 我们不应该在解析响应体之前设置响应头，因为解析部分可能失败，
	// 这样我们将不得不发送错误响应，但此时，响应头已经设置，
	// 这会让 httpClient 感到困惑，例如 Postman 会报告错误，我们无法查看响应内容。
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	resp.Body.Close()

	// 解析 usage 信息
	var usage dto.Usage
	var responseMap map[string]interface{}
	err = json.Unmarshal(responseBody, &responseMap)
	if err == nil {
		if usageMap, ok := responseMap["usage"].(map[string]interface{}); ok {
			usage.PromptTokens = int(usageMap["prompt_tokens"].(float64))
			usage.CompletionTokens = int(usageMap["completion_tokens"].(float64))
			usage.TotalTokens = int(usageMap["total_tokens"].(float64))
		} else {
			// 如果没有 usage 信息，手动计算 completionTokens
			usage.PromptTokens = promptTokens
			usage.CompletionTokens = 0
			if choices, ok := responseMap["choices"].([]interface{}); ok {
				for _, choice := range choices {
					if choiceMap, ok := choice.(map[string]interface{}); ok {
						// 处理 message.content
						if message, ok := choiceMap["message"].(map[string]interface{}); ok {
							if content, ok := message["content"].(string); ok {
								ctkm, _ := service.CountTextToken(content, model)
								usage.CompletionTokens += ctkm
							}
						}
					}
				}
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	} else {
		// 如果解析失败，仍然返回初始的 usage
		usage = dto.Usage{
			PromptTokens: promptTokens,
			TotalTokens:  promptTokens,
		}
	}

	return nil, &usage
}

func OpenaiTTSHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	// Reset response body
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}

	usage := &dto.Usage{}
	usage.PromptTokens = info.PromptTokens
	usage.TotalTokens = info.PromptTokens
	return nil, usage
}

func OpenaiSTTHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, responseFormat string) (*dto.OpenAIErrorWithStatusCode, *dto.Usage) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError), nil
	}
	err = resp.Body.Close()
	if err != nil {
		return service.OpenAIErrorWrapper(err, "close_response_body_failed", http.StatusInternalServerError), nil
	}
	// Reset response body
	resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	for k, v := range resp.Header {
		c.Writer.Header().Set(k, v[0])
	}
	c.Writer.WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	if err != nil {
		return service.OpenAIErrorWrapper(err, "copy_response_body_failed", http.StatusInternalServerError), nil
	}
	resp.Body.Close()

	var text string
	switch responseFormat {
	case "json":
		text, err = getTextFromJSON(responseBody)
	case "text":
		text, err = getTextFromText(responseBody)
	case "srt":
		text, err = getTextFromSRT(responseBody)
	case "verbose_json":
		text, err = getTextFromVerboseJSON(responseBody)
	case "vtt":
		text, err = getTextFromVTT(responseBody)
	}

	usage := &dto.Usage{}
	usage.PromptTokens = info.PromptTokens
	usage.CompletionTokens, _ = service.CountTextToken(text, info.UpstreamModelName)
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return nil, usage
}

func getTextFromVTT(body []byte) (string, error) {
	return getTextFromSRT(body)
}

func getTextFromVerboseJSON(body []byte) (string, error) {
	var whisperResponse dto.WhisperVerboseJSONResponse
	if err := json.Unmarshal(body, &whisperResponse); err != nil {
		return "", fmt.Errorf("unmarshal_response_body_failed err :%w", err)
	}
	return whisperResponse.Text, nil
}

func getTextFromSRT(body []byte) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	var builder strings.Builder
	var textLine bool
	for scanner.Scan() {
		line := scanner.Text()
		if textLine {
			builder.WriteString(line)
			textLine = false
			continue
		} else if strings.Contains(line, "-->") {
			textLine = true
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return builder.String(), nil
}

func getTextFromText(body []byte) (string, error) {
	return strings.TrimSuffix(string(body), "\n"), nil
}

func getTextFromJSON(body []byte) (string, error) {
	var whisperResponse dto.AudioResponse
	if err := json.Unmarshal(body, &whisperResponse); err != nil {
		return "", fmt.Errorf("unmarshal_response_body_failed err :%w", err)
	}
	return whisperResponse.Text, nil
}

func OpenaiRealtimeHandler(c *gin.Context, info *relaycommon.RelayInfo) (*dto.OpenAIErrorWithStatusCode, *dto.RealtimeUsage) {
	info.IsStream = true
	clientConn := info.ClientWs
	targetConn := info.TargetWs

	clientClosed := make(chan struct{})
	targetClosed := make(chan struct{})
	sendChan := make(chan []byte, 100)
	receiveChan := make(chan []byte, 100)
	errChan := make(chan error, 2)

	usage := &dto.RealtimeUsage{}
	localUsage := &dto.RealtimeUsage{}
	sumUsage := &dto.RealtimeUsage{}

	gopool.Go(func() {
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := clientConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from client: %v", err)
					}
					close(clientClosed)
					return
				}

				realtimeEvent := &dto.RealtimeEvent{}
				err = json.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdate {
					if realtimeEvent.Session != nil {
						if realtimeEvent.Session.Tools != nil {
							info.RealtimeTools = realtimeEvent.Session.Tools
						}
					}
				}

				textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
				if err != nil {
					errChan <- fmt.Errorf("error counting text token: %v", err)
					return
				}
				common.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
				localUsage.TotalTokens += textToken + audioToken
				localUsage.InputTokens += textToken + audioToken
				localUsage.InputTokenDetails.TextTokens += textToken
				localUsage.InputTokenDetails.AudioTokens += audioToken

				err = service.WssString(c, targetConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to target: %v", err)
					return
				}

				select {
				case sendChan <- message:
				default:
				}
			}
		}
	})

	gopool.Go(func() {
		for {
			select {
			case <-c.Done():
				return
			default:
				_, message, err := targetConn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						errChan <- fmt.Errorf("error reading from target: %v", err)
					}
					close(targetClosed)
					return
				}
				info.SetFirstResponseTime()
				realtimeEvent := &dto.RealtimeEvent{}
				err = json.Unmarshal(message, realtimeEvent)
				if err != nil {
					errChan <- fmt.Errorf("error unmarshalling message: %v", err)
					return
				}

				if realtimeEvent.Type == dto.RealtimeEventTypeResponseDone {
					realtimeUsage := realtimeEvent.Response.Usage
					if realtimeUsage != nil {
						usage.TotalTokens += realtimeUsage.TotalTokens
						usage.InputTokens += realtimeUsage.InputTokens
						usage.OutputTokens += realtimeUsage.OutputTokens
						usage.InputTokenDetails.AudioTokens += realtimeUsage.InputTokenDetails.AudioTokens
						usage.InputTokenDetails.CachedTokens += realtimeUsage.InputTokenDetails.CachedTokens
						usage.InputTokenDetails.TextTokens += realtimeUsage.InputTokenDetails.TextTokens
						usage.OutputTokenDetails.AudioTokens += realtimeUsage.OutputTokenDetails.AudioTokens
						usage.OutputTokenDetails.TextTokens += realtimeUsage.OutputTokenDetails.TextTokens
						err := preConsumeUsage(c, info, usage, sumUsage)
						if err != nil {
							errChan <- fmt.Errorf("error consume usage: %v", err)
							return
						}
						// 本次计费完成，清除
						usage = &dto.RealtimeUsage{}

						localUsage = &dto.RealtimeUsage{}
					} else {
						textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
						if err != nil {
							errChan <- fmt.Errorf("error counting text token: %v", err)
							return
						}
						common.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
						localUsage.TotalTokens += textToken + audioToken
						info.IsFirstRequest = false
						localUsage.InputTokens += textToken + audioToken
						localUsage.InputTokenDetails.TextTokens += textToken
						localUsage.InputTokenDetails.AudioTokens += audioToken
						err = preConsumeUsage(c, info, localUsage, sumUsage)
						if err != nil {
							errChan <- fmt.Errorf("error consume usage: %v", err)
							return
						}
						// 本次计费完成，清除
						localUsage = &dto.RealtimeUsage{}
						// print now usage
					}
					//common.LogInfo(c, fmt.Sprintf("realtime streaming sumUsage: %v", sumUsage))
					//common.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))
					//common.LogInfo(c, fmt.Sprintf("realtime streaming localUsage: %v", localUsage))

				} else if realtimeEvent.Type == dto.RealtimeEventTypeSessionUpdated || realtimeEvent.Type == dto.RealtimeEventTypeSessionCreated {
					realtimeSession := realtimeEvent.Session
					if realtimeSession != nil {
						// update audio format
						info.InputAudioFormat = common.GetStringIfEmpty(realtimeSession.InputAudioFormat, info.InputAudioFormat)
						info.OutputAudioFormat = common.GetStringIfEmpty(realtimeSession.OutputAudioFormat, info.OutputAudioFormat)
					}
				} else {
					textToken, audioToken, err := service.CountTokenRealtime(info, *realtimeEvent, info.UpstreamModelName)
					if err != nil {
						errChan <- fmt.Errorf("error counting text token: %v", err)
						return
					}
					common.LogInfo(c, fmt.Sprintf("type: %s, textToken: %d, audioToken: %d", realtimeEvent.Type, textToken, audioToken))
					localUsage.TotalTokens += textToken + audioToken
					localUsage.OutputTokens += textToken + audioToken
					localUsage.OutputTokenDetails.TextTokens += textToken
					localUsage.OutputTokenDetails.AudioTokens += audioToken
				}

				err = service.WssString(c, clientConn, string(message))
				if err != nil {
					errChan <- fmt.Errorf("error writing to client: %v", err)
					return
				}

				select {
				case receiveChan <- message:
				default:
				}
			}
		}
	})

	select {
	case <-clientClosed:
	case <-targetClosed:
	case err := <-errChan:
		//return service.OpenAIErrorWrapper(err, "realtime_error", http.StatusInternalServerError), nil
		common.LogError(c, "realtime error: "+err.Error())
	case <-c.Done():
	}

	if usage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, usage, sumUsage)
	}

	if localUsage.TotalTokens != 0 {
		_ = preConsumeUsage(c, info, localUsage, sumUsage)
	}

	// check usage total tokens, if 0, use local usage

	return nil, sumUsage
}

func preConsumeUsage(ctx *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage, totalUsage *dto.RealtimeUsage) error {
	totalUsage.TotalTokens += usage.TotalTokens
	totalUsage.InputTokens += usage.InputTokens
	totalUsage.OutputTokens += usage.OutputTokens
	totalUsage.InputTokenDetails.CachedTokens += usage.InputTokenDetails.CachedTokens
	totalUsage.InputTokenDetails.TextTokens += usage.InputTokenDetails.TextTokens
	totalUsage.InputTokenDetails.AudioTokens += usage.InputTokenDetails.AudioTokens
	totalUsage.OutputTokenDetails.TextTokens += usage.OutputTokenDetails.TextTokens
	totalUsage.OutputTokenDetails.AudioTokens += usage.OutputTokenDetails.AudioTokens
	// clear usage
	err := service.PreWssConsumeQuota(ctx, info, usage)
	return err
}
