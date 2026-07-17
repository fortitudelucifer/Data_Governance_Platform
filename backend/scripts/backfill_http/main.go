package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type loginResponse struct {
	Token string `json:"token"`
}

type datasetItem struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type docListItem struct {
	DocKey string `json:"doc_key"`
}

type docListResponse struct {
	Items []docListItem `json:"items"`
	Total int64         `json:"total"`
}

type documentResponse struct {
	DocKey string                 `json:"doc_key"`
	Data   map[string]interface{} `json:"data"`
}

type updatePayload struct {
	Data map[string]interface{} `json:"data"`
}

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8380", "API base URL")
	username := flag.String("username", "admin", "login username")
	password := flag.String("password", "admin123", "login password")
	datasetID := flag.Uint("dataset-id", 0, "specific dataset id, 0 means all")
	pageSize := flag.Int("page-size", 100, "page size for listing documents")
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second}
	token, err := login(client, *baseURL, *username, *password)
	must(err)

	datasets, err := listDatasets(client, *baseURL, token)
	must(err)

	updatedDocs := 0
	skippedDocs := 0
	failedDocs := 0

	for _, ds := range datasets {
		if *datasetID != 0 && ds.ID != *datasetID {
			continue
		}
		fmt.Printf("dataset %d %s\n", ds.ID, ds.Name)
		docKeys, err := collectDocKeys(client, *baseURL, token, ds.ID, *pageSize)
		if err != nil {
			fmt.Printf("  collect doc keys error: %v\n", err)
			failedDocs++
			continue
		}
		for _, docKey := range docKeys {
			doc, err := getDocument(client, *baseURL, token, docKey)
			if err != nil {
				fmt.Printf("  get %s error: %v\n", docKey, err)
				failedDocs++
				continue
			}
			changed := normalizeDocumentQAPairs(doc.Data)
			if !changed {
				skippedDocs++
				continue
			}
			if err := updateDocument(client, *baseURL, token, docKey, doc.Data); err != nil {
				fmt.Printf("  update %s error: %v\n", docKey, err)
				failedDocs++
				continue
			}
			updatedDocs++
		}
	}

	fmt.Printf("done updated=%d skipped=%d failed=%d\n", updatedDocs, skippedDocs, failedDocs)
}

func login(client *http.Client, baseURL, username, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := client.Post(strings.TrimRight(baseURL, "/")+"/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("login status=%d body=%s", resp.StatusCode, string(body))
	}
	var out loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("empty token")
	}
	return out.Token, nil
}

func listDatasets(client *http.Client, baseURL, token string) ([]datasetItem, error) {
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/datasets", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list datasets status=%d body=%s", resp.StatusCode, string(body))
	}
	var out []datasetItem
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func listDocuments(client *http.Client, baseURL, token string, datasetID uint, page, pageSize int) (*docListResponse, error) {
	u := fmt.Sprintf("%s/datasets/%d/documents?page=%d&page_size=%d", strings.TrimRight(baseURL, "/"), datasetID, page, pageSize)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list documents status=%d body=%s", resp.StatusCode, string(body))
	}
	var out docListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func collectDocKeys(client *http.Client, baseURL, token string, datasetID uint, pageSize int) ([]string, error) {
	page := 1
	var docKeys []string
	for {
		resp, err := listDocuments(client, baseURL, token, datasetID, page, pageSize)
		if err != nil {
			return nil, err
		}
		if len(resp.Items) == 0 {
			break
		}
		for _, item := range resp.Items {
			docKeys = append(docKeys, item.DocKey)
		}
		if int64(page*pageSize) >= resp.Total {
			break
		}
		page++
	}
	return docKeys, nil
}

func getDocument(client *http.Client, baseURL, token, docKey string) (*documentResponse, error) {
	u := strings.TrimRight(baseURL, "/") + "/documents/" + url.PathEscape(docKey)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get document status=%d body=%s", resp.StatusCode, string(body))
	}
	var out documentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func updateDocument(client *http.Client, baseURL, token, docKey string, data map[string]interface{}) error {
	payload, _ := json.Marshal(updatePayload{Data: data})
	u := strings.TrimRight(baseURL, "/") + "/documents/" + url.PathEscape(docKey) + "/update"
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

func normalizeDocumentQAPairs(data map[string]interface{}) bool {
	if data == nil {
		return false
	}
	rawPairs, ok := data["qa_pairs"]
	if !ok || rawPairs == nil {
		return false
	}
	qaList, ok := rawPairs.([]interface{})
	if !ok {
		return false
	}
	changed := false
	for i, item := range qaList {
		pairMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		answerRaw, hasAnswer := pairMap["answer"]
		if !hasAnswer {
			continue
		}
		normalized, structured := normalizeAnswerValue(answerRaw)
		currentAnswer := strings.TrimSpace(fmt.Sprintf("%v", pairMap["answer"]))
		if normalized != "" && normalized != currentAnswer {
			pairMap["answer"] = normalized
			changed = true
		}
		if structured != nil {
			meta, _ := pairMap["meta"].(map[string]interface{})
			if meta == nil {
				meta = map[string]interface{}{}
			}
			if _, ok := meta["raw_answer"]; !ok || fmt.Sprintf("%v", meta["raw_answer"]) != fmt.Sprintf("%v", answerRaw) {
				meta["raw_answer"] = answerRaw
				changed = true
			}
			meta["answer_structured"] = structured
			pairMap["meta"] = meta
		}
		qaList[i] = pairMap
	}
	if changed {
		data["qa_pairs"] = qaList
	}
	return changed
}

func normalizeAnswerValue(raw interface{}) (string, interface{}) {
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "", nil
		}
		if parsed, ok := tryParseStructuredString(trimmed); ok {
			return structuredToReadable(parsed), parsed
		}
		return trimmed, nil
	case map[string]interface{}, []interface{}:
		return structuredToReadable(v), v
	default:
		return fmt.Sprintf("%v", raw), nil
	}
}

func tryParseStructuredString(s string) (interface{}, bool) {
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return nil, false
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func structuredToReadable(v interface{}) string {
	switch x := v.(type) {
	case []interface{}:
		if allStrings(x) {
			parts := make([]string, 0, len(x))
			for _, item := range x {
				s := strings.TrimSpace(fmt.Sprintf("%v", item))
				if s != "" {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, "；")
		}
		parts := make([]string, 0, len(x))
		for _, item := range x {
			txt := strings.TrimSpace(structuredToReadable(item))
			if txt != "" {
				parts = append(parts, txt)
			}
		}
		return strings.Join(parts, "；")
	case map[string]interface{}:
		parts := make([]string, 0, 8)
		if cause := cleanString(x["conviction_or_cause"]); cause != "" {
			parts = append(parts, fmt.Sprintf("案由：%s", cause))
		}
		if result := cleanString(x["result_type"]); result != "" {
			parts = append(parts, fmt.Sprintf("裁判结果：%s", result))
		}
		if statutes, ok := asStringSlice(x["法条"]); ok && len(statutes) > 0 {
			parts = append(parts, fmt.Sprintf("涉及法条：%s", strings.Join(statutes, "；")))
		}
		if pays, ok := x["赔偿/给付金额"]; ok {
			if arr, ok := pays.([]interface{}); ok {
				items := make([]string, 0, len(arr))
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						typeText := cleanString(m["类型"])
						amount := cleanString(m["金额_元"])
						if typeText != "" && amount != "" {
							items = append(items, fmt.Sprintf("%s%s元", typeText, amount))
						} else if amount != "" {
							items = append(items, fmt.Sprintf("%s元", amount))
						}
					}
				}
				if len(items) > 0 {
					parts = append(parts, fmt.Sprintf("金额：%s", strings.Join(items, "；")))
				}
			}
		}
		if amount := cleanString(x["赔偿/给付金额_元"]); amount != "" {
			parts = append(parts, fmt.Sprintf("给付金额：%s元", amount))
		}
		if sentence, ok := x["sentence"].(map[string]interface{}); ok {
			if penalties, ok := sentence["penalty_types"].([]interface{}); ok && len(penalties) > 0 {
				names := make([]string, 0, len(penalties))
				for _, item := range penalties {
					s := cleanString(item)
					if s != "" {
						names = append(names, s)
					}
				}
				if len(names) > 0 {
					parts = append(parts, fmt.Sprintf("处理方式：%s", strings.Join(names, "、")))
				}
			}
			if compensation := cleanString(sentence["compensation_amount"]); compensation != "" {
				parts = append(parts, fmt.Sprintf("给付金额：%s元", compensation))
			}
			if fine := cleanString(sentence["fine_amount"]); fine != "" {
				parts = append(parts, fmt.Sprintf("罚金：%s元", fine))
			}
			if term := cleanString(sentence["term_months"]); term != "" {
				parts = append(parts, fmt.Sprintf("期限：%s个月", term))
			}
			if other := cleanString(sentence["other"]); other != "" {
				parts = append(parts, fmt.Sprintf("说明：%s", other))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "；")
		}
		parts = parts[:0]
		for key, val := range x {
			txt := structuredToReadable(val)
			if txt != "" {
				parts = append(parts, fmt.Sprintf("%s：%s", key, txt))
			}
		}
		return strings.Join(parts, "；")
	default:
		return cleanString(v)
	}
}

func asStringSlice(v interface{}) ([]string, bool) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := cleanString(item)
		if s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

func allStrings(items []interface{}) bool {
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func cleanString(v interface{}) string {
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if s == "" || s == "<nil>" || s == "null" {
		return ""
	}
	return s
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
