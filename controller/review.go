package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// ReviewResult 审查结果结构
type ReviewResult struct {
	Results []struct {
		Violation string `json:"被标记为违规"`
	} `json:"审查结果"`
}

// CheckContent 审查内容函数
func CheckContent(content string) (bool, error) {
	reviewURL := "https://check.openai.wisdgod.com/check"
	requestBody, err := json.Marshal(map[string]string{
		"content": content,
	})
	if err != nil {
		return false, err
	}

	resp, err := http.Post(reviewURL, "application/json", bytes.NewBuffer(requestBody))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result ReviewResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	// 假设只要结果中有一个被标记为违规，就返回 true
	for _, r := range result.Results {
		if r.Violation == "是" {
			return true, nil
		}
	}
	return false, nil
}
