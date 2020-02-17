package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SolrContext contains data related to the Solr API connection
type SolrContext struct {
	client *http.Client
	url    string
}

// ServiceContext contains common data used by all handlers
type ServiceContext struct {
	config         *ServiceConfig
	solr           *SolrContext
	maxSuggestions int
}

func integerWithMinimum(str string, min int) int {
	val, err := strconv.Atoi(str)

	// fallback for invalid or nonsensical values
	if err != nil || val < min {
		val = min
	}

	return val
}

// InitializeService will initialize the service context based on the config parameters.
func InitializeService(cfg *ServiceConfig) *ServiceContext {
	log.Printf("Initializing Service")

	connTimeout := integerWithMinimum(cfg.SolrConnTimeout, 5)
	readTimeout := integerWithMinimum(cfg.SolrReadTimeout, 5)

	solrClient := &http.Client{
		Timeout: time.Duration(readTimeout) * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   time.Duration(connTimeout) * time.Second,
				KeepAlive: 60 * time.Second,
			}).DialContext,
			MaxIdleConns:        100, // we are hitting one solr host, so
			MaxIdleConnsPerHost: 100, // these two values can be the same
			IdleConnTimeout:     90 * time.Second,
		},
	}

	solr := SolrContext{
		url:    fmt.Sprintf("%s/%s/%s", cfg.SolrHost, cfg.SolrCore, cfg.SolrHandler),
		client: solrClient,
	}

	svc := ServiceContext{
		config:         cfg,
		solr:           &solr,
		maxSuggestions: integerWithMinimum(cfg.MaxSuggestions, 1),
	}

	log.Printf("[SERVICE] solr.url = [%s]", svc.solr.url)

	return &svc
}

// IgnoreHandler is a dummy to handle certain browser requests without warnings (e.g. favicons)
func (svc *ServiceContext) IgnoreHandler(c *gin.Context) {
}

// VersionHandler reports the version of the service
func (svc *ServiceContext) VersionHandler(c *gin.Context) {
	build := "missing"

	files, _ := filepath.Glob("buildtag.*")
	if len(files) == 1 {
		build = strings.Replace(files[0], "buildtag.", "", 1)
	}

	vMap := make(map[string]string)

	vMap["build"] = build
	vMap["go_version"] = fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	vMap["git_commit"] = GitCommit

	c.JSON(http.StatusOK, vMap)
}

// HealthCheckHandler reports the health of the serivce
func (svc *ServiceContext) HealthCheckHandler(c *gin.Context) {
	type hcResp struct {
		Healthy bool   `json:"healthy"`
		Message string `json:"message,omitempty"`
	}

	hcMap := make(map[string]hcResp)

	hcSolr := hcResp{}

	status := http.StatusOK
	hcSolr = hcResp{Healthy: true}

	s := InitializeSuggestion(svc)
	s.req.Query = "keyword:{pingtest}"

	if _, err := s.HandleSuggestionRequest(); err != nil {
		status = http.StatusInternalServerError
		hcSolr = hcResp{Healthy: false, Message: err.Error()}
	}

	hcMap["solr"] = hcSolr

	c.JSON(status, hcMap)
}

// SuggestionHandler takes a keyword search and suggests alternate searches
// that may provide better or more focused results
func (svc *ServiceContext) SuggestionHandler(c *gin.Context) {
	s := InitializeSuggestion(svc)

	if err := c.BindJSON(&s.req); err != nil {
		log.Printf("SuggestionHandler: invalid request: %s", err.Error())
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}

	suggestions, _ := s.HandleSuggestionRequest()

	c.JSON(http.StatusOK, suggestions)
}
