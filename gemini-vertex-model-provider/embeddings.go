package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIEmbeddingRequest - not (yet) provided by the Chat Completion Client package
type openAIEmbeddingRequest struct {
	Input          string `json:"input"`
	Model          string `json:"model"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
}

type openAIResponse struct {
	Data []openAIResponseData `json:"data"`
}

type openAIResponseData struct {
	Embedding []float32 `json:"embedding"`
}

type vertexEmbeddingResponse struct {
	Predictions []vertexPrediction `json:"predictions"`
}

type vertexPrediction struct {
	Embeddings vertexEmbeddings `json:"embeddings"`
}

type vertexEmbeddings struct {
	Values []float32 `json:"values"`
	// There's more here in the actual response, but we only care about the embeddings
}

type server struct {
	httpClient *http.Client
	location   string
	projectID  string
}

// embeddings - not (yet) provided by the Google GenAI package
func (s *server) embeddings(w http.ResponseWriter, r *http.Request) {
	var er openAIEmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&er); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict", s.location, s.projectID, s.location, strings.TrimPrefix(er.Model, "google/"))

	params := make(map[string]any)
	if er.Dimensions != nil {
		params["outputDimensionality"] = *er.Dimensions
	}

	payload := map[string]any{
		"instances": []map[string]any{
			{
				"task_type":  "QUESTION_ANSWERING",
				"content":    er.Input,
				"parameters": params,
			},
		},
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, fmt.Sprintf("couldn't marshal request body: %v", err), http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewBuffer(reqBody))
	if err != nil {
		http.Error(w, fmt.Sprintf("couldn't create request: %v", err), http.StatusInternalServerError)
		return
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("couldn't make request: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var b []byte
		b, _ = io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("unexpected status code %d on url %q: %s", resp.StatusCode, url, b), resp.StatusCode)
		return
	}

	var embeddingResponse vertexEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embeddingResponse); err != nil {
		http.Error(w, fmt.Sprintf("couldn't decode response: %v", err), http.StatusInternalServerError)
		return
	}

	if len(embeddingResponse.Predictions) == 0 || len(embeddingResponse.Predictions[0].Embeddings.Values) == 0 {
		http.Error(w, "no embeddings found in the response", http.StatusInternalServerError)
		return
	}

	if len(embeddingResponse.Predictions) > 1 {
		fmt.Println("Info: multiple predictions found in the response - using only the first one")
	}

	oaiResp := openAIResponse{
		Data: []openAIResponseData{
			{
				Embedding: embeddingResponse.Predictions[0].Embeddings.Values,
			},
		},
	}

	if err := json.NewEncoder(w).Encode(oaiResp); err != nil {
		http.Error(w, fmt.Sprintf("couldn't encode response: %v", err), http.StatusInternalServerError)
		return
	}
}
