package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// SolrRequestParams contains the parameters for a JSON Solr API request
type SolrRequestParams struct {
	Debug      bool     `json:"debug,omitempty"`
	DefType    string   `json:"defType,omitempty"`
	Start      int      `json:"start"`
	Rows       int      `json:"rows"`
	Fl         []string `json:"fl,omitempty"`
	Q          string   `json:"q,omitempty"`
	Qf         string   `json:"qf,omitempty"`
	Sort       string   `json:"sort,omitempty"`
	DebugQuery string   `json:"debugQuery,omitempty"`
}

// SolrRequestJSON contains the data for a JSON Solr API request
type SolrRequestJSON struct {
	Params SolrRequestParams `json:"params"`
}

// SolrRequest contains the info needed to perform a request against the Solr API.
// This is structured as a JSON API request, but its values can be stuffed into
// query parameters instead
type SolrRequest struct {
	json SolrRequestJSON
}

// SolrResponseHeader contains the header portion of the response from the Solr API
type SolrResponseHeader struct {
	Status int `json:"status,omitempty"`
	QTime  int `json:"QTime,omitempty"`
}

// SolrDocument is a single result record for a Solr request
type SolrDocument struct {
	Phrase string  `json:"phrase,omitempty"`
	Type   string  `json:"type,omitempty"`
	Count  int     `json:"count,omitempty"`
	Score  float64 `json:"score,omitempty"`
}

// SolrResponseDocuments is a set of result records for a Solr request, along with some metadata
type SolrResponseDocuments struct {
	NumFound int            `json:"numFound,omitempty"`
	Start    int            `json:"start,omitempty"`
	MaxScore float64        `json:"maxScore,omitempty"`
	Docs     []SolrDocument `json:"docs,omitempty"`
}

// SolrError contains the error portion of the response from the Solr API, when a failure occurs
type SolrError struct {
	Metadata []string `json:"metadata,omitempty"`
	Msg      string   `json:"msg,omitempty"`
	Code     int      `json:"code,omitempty"`
}

// SolrResponse contains the response from the Solr API
type SolrResponse struct {
	ResponseHeader SolrResponseHeader    `json:"responseHeader,omitempty"`
	Response       SolrResponseDocuments `json:"response,omitempty"`
	Error          SolrError             `json:"error,omitempty"`
	Debug          interface{}           `json:"debug,omitempty"`
	Status         string                `json:"status,omitempty"`
}

// SolrQuery performs an API request against Solr and returns the response, or an error
func (s *SuggestionContext) SolrQuery(solrReq *SolrRequest) (*SolrResponse, error) {
	ctx := s.svc.solr.service

	var req *http.Request
	var reqErr error

	reqType := "GET"

	if reqType == "POST" {
		jsonBytes, jsonErr := json.Marshal(solrReq.json)
		if jsonErr != nil {
			log.Printf("Marshal() failed: %s", jsonErr.Error())
			return nil, fmt.Errorf("failed to marshal Solr JSON")
		}

		log.Printf("[SOLR] %s req: [%s]", reqType, string(jsonBytes))

		if req, reqErr = http.NewRequest(reqType, ctx.url, bytes.NewBuffer(jsonBytes)); reqErr != nil {
			log.Printf("NewRequest() failed: %s", reqErr.Error())
			return nil, fmt.Errorf("failed to create Solr request")
		}

		req.Header.Set("Content-Type", "application/json")
	} else {
		if req, reqErr = http.NewRequest(reqType, ctx.url, nil); reqErr != nil {
			log.Printf("NewRequest() failed: %s", reqErr.Error())
			return nil, fmt.Errorf("failed to create Solr request")
		}

		q := req.URL.Query()

		q.Add("q", solrReq.json.Params.Q)
		q.Add("start", fmt.Sprintf("%d", solrReq.json.Params.Start))
		q.Add("rows", fmt.Sprintf("%d", solrReq.json.Params.Rows))
		q.Add("sort", solrReq.json.Params.Sort)
		q.Add("defType", solrReq.json.Params.DefType)
		q.Add("qf", solrReq.json.Params.Qf)

		for _, val := range solrReq.json.Params.Fl {
			q.Add("fl", val)
		}

		req.URL.RawQuery = q.Encode()

		log.Printf("[SOLR] %s req: [%s?%s]", reqType, ctx.url, req.URL.RawQuery)
	}

	start := time.Now()
	res, resErr := ctx.client.Do(req)
	elapsedMS := int64(time.Since(start) / time.Millisecond)

	// external service failure logging (scenario 1)

	if resErr != nil {
		status := http.StatusBadRequest
		errMsg := resErr.Error()
		if strings.Contains(errMsg, "Timeout") {
			status = http.StatusRequestTimeout
			errMsg = fmt.Sprintf("%s timed out", ctx.url)
		} else if strings.Contains(errMsg, "connection refused") {
			status = http.StatusServiceUnavailable
			errMsg = fmt.Sprintf("%s refused connection", ctx.url)
		}

		log.Printf("client.Do() failed: %s", resErr.Error())
		log.Printf("ERROR: Failed response from %s %s - %d:%s. Elapsed Time: %d (ms)", reqType, ctx.url, status, errMsg, elapsedMS)
		return nil, fmt.Errorf("failed to receive Solr response")
	}

	log.Printf("[SOLR] http res: %5d ms", int64(time.Since(start)/time.Millisecond))

	defer res.Body.Close()

	var solrRes SolrResponse

	// parse response from stream

	decoder := json.NewDecoder(res.Body)

	// external service failure logging (scenario 2)

	start = time.Now()
	if decErr := decoder.Decode(&solrRes); decErr != nil {
		log.Printf("Decode() failed: %s", decErr.Error())
		log.Printf("ERROR: Failed response from %s %s - %d:%s. Elapsed Time: %d (ms)", reqType, ctx.url, http.StatusInternalServerError, decErr.Error(), elapsedMS)
		return nil, fmt.Errorf("failed to decode Solr response")
	}
	log.Printf("[SOLR] json dec: %5d ms", int64(time.Since(start)/time.Millisecond))

	// external service success logging

	log.Printf("Successful Solr response from %s %s. Elapsed Time: %d (ms)", reqType, ctx.url, elapsedMS)

	// log abbreviated results

	logHeader := fmt.Sprintf("[SOLR] res: header: { status = %d, QTime = %d }", solrRes.ResponseHeader.Status, solrRes.ResponseHeader.QTime)

	// quick validation
	if solrRes.ResponseHeader.Status != 0 {
		log.Printf("%s, error: { code = %d, msg = %s }", logHeader, solrRes.Error.Code, solrRes.Error.Msg)
		return nil, fmt.Errorf("%d - %s", solrRes.Error.Code, solrRes.Error.Msg)
	}

	log.Printf("%s, { start = %d, rows = %d, total = %d, maxScore = %0.2f }", logHeader, solrRes.Response.Start, len(solrRes.Response.Docs), solrRes.Response.NumFound, solrRes.Response.MaxScore)

	return &solrRes, nil
}

// SolrPing performs a ping request against Solr and returns an error if any issues are detected
func (s *SuggestionContext) SolrPing() error {
	ctx := s.svc.solr.healthcheck

	req, reqErr := http.NewRequest("GET", ctx.url, nil)
	if reqErr != nil {
		log.Printf("[SOLR] NewRequest() failed: %s", reqErr.Error())
		return fmt.Errorf("failed to create Solr request")
	}

	start := time.Now()
	res, resErr := ctx.client.Do(req)
	elapsedMS := int64(time.Since(start) / time.Millisecond)

	// external service failure logging (scenario 1)

	if resErr != nil {
		status := http.StatusBadRequest
		errMsg := resErr.Error()
		if strings.Contains(errMsg, "Timeout") {
			status = http.StatusRequestTimeout
			errMsg = fmt.Sprintf("%s timed out", ctx.url)
		} else if strings.Contains(errMsg, "connection refused") {
			status = http.StatusServiceUnavailable
			errMsg = fmt.Sprintf("%s refused connection", ctx.url)
		}

		log.Printf("[SOLR] client.Do() failed: %s", resErr.Error())
		log.Printf("ERROR: Failed response from %s %s - %d:%s. Elapsed Time: %d (ms)", req.Method, ctx.url, status, errMsg, elapsedMS)
		return fmt.Errorf("failed to receive Solr response")
	}

	defer res.Body.Close()

	var solrRes SolrResponse

	decoder := json.NewDecoder(res.Body)

	// external service failure logging (scenario 2)

	if decErr := decoder.Decode(&solrRes); decErr != nil {
		log.Printf("[SOLR] Decode() failed: %s", decErr.Error())
		log.Printf("ERROR: Failed response from %s %s - %d:%s. Elapsed Time: %d (ms)", req.Method, ctx.url, http.StatusInternalServerError, decErr.Error(), elapsedMS)
		return fmt.Errorf("failed to decode Solr response")
	}

	// external service success logging

	log.Printf("Successful Solr response from %s %s. Elapsed Time: %d (ms)", req.Method, ctx.url, elapsedMS)

	logHeader := fmt.Sprintf("[SOLR] res: header: { status = %d, QTime = %d }", solrRes.ResponseHeader.Status, solrRes.ResponseHeader.QTime)

	// quick validation
	if solrRes.ResponseHeader.Status != 0 {
		log.Printf("%s, error: { code = %d, msg = %s }", logHeader, solrRes.Error.Code, solrRes.Error.Msg)
		return fmt.Errorf("%d - %s", solrRes.Error.Code, solrRes.Error.Msg)
	}

	log.Printf("%s, ping status: %s", logHeader, solrRes.Status)

	if solrRes.Status != "OK" {
		return fmt.Errorf("ping status was not OK")
	}

	return nil
}
