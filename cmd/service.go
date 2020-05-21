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

// ServiceSolrContext contains data related to the Solr API connection
type ServiceSolrContext struct {
	client *http.Client
	url    string
}

// ServiceSolr contains data related to the Solr API connection
type ServiceSolr struct {
	service     ServiceSolrContext
	healthcheck ServiceSolrContext
}

// ServiceContext contains common data used by all handlers
type ServiceContext struct {
	config *serviceConfig
	solr   ServiceSolr
}

func integerWithMinimum(str string, min int) int {
	val, err := strconv.Atoi(str)

	// fallback for invalid or nonsensical values
	if err != nil || val < min {
		val = min
	}

	return val
}

func solrContextFromConfig(host, core string, cfg *serviceConfigSolrClient) ServiceSolrContext {
	connTimeout := integerWithMinimum(cfg.ConnTimeout, 1)
	readTimeout := integerWithMinimum(cfg.ReadTimeout, 1)

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

	ctx := ServiceSolrContext{
		url:    fmt.Sprintf("%s/%s/%s", host, core, cfg.Endpoint),
		client: solrClient,
	}

	log.Printf("[SERVICE] solr context url = [%s]", ctx.url)

	return ctx
}

// InitializeService will initialize the service context based on the config parameters.
func InitializeService(cfg *serviceConfig) *ServiceContext {
	log.Printf("initializing service")

	svcCtx := solrContextFromConfig(cfg.Solr.Host, cfg.Solr.Core, &cfg.Solr.Clients.Service)
	hcCtx := solrContextFromConfig(cfg.Solr.Host, cfg.Solr.Core, &cfg.Solr.Clients.HealthCheck)

	solr := ServiceSolr{
		service:     svcCtx,
		healthcheck: hcCtx,
	}

	svc := ServiceContext{
		config: cfg,
		solr:   solr,
	}

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
	s := InitializeSuggestion(svc)

	ping := s.HandlePingRequest()

	// build response

	internalServiceError := false

	type hcResp struct {
		Healthy bool   `json:"healthy"`
		Message string `json:"message,omitempty"`
	}

	hcSolr := hcResp{Healthy: true}
	if ping != nil {
		internalServiceError = true
		hcSolr = hcResp{Healthy: false, Message: ping.Error()}
	}

	hcMap := make(map[string]hcResp)
	hcMap["solr"] = hcSolr

	hcStatus := http.StatusOK
	if internalServiceError == true {
		hcStatus = http.StatusInternalServerError
	}

	hcMap["solr"] = hcSolr

	c.JSON(hcStatus, hcMap)
}

// AuthorSuggestionHandler takes a keyword search and suggests alternate
// author searches that may provide better or more focused results
func (svc *ServiceContext) AuthorSuggestionHandler(c *gin.Context) {
	s := InitializeSuggestion(svc)

	if err := c.BindJSON(&s.req); err != nil {
		log.Printf("AuthorSuggestionHandler: invalid request: %s", err.Error())
		c.String(http.StatusBadRequest, "Invalid request")
		return
	}

	suggestions, err := s.HandleAuthorSuggestionRequest()

	if err != nil {
		log.Printf("ERROR: %s", err.Error())
	}

	c.JSON(http.StatusOK, suggestions)
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

	suggestions, err := s.HandleSuggestionRequest()

	if err != nil {
		log.Printf("ERROR: %s", err.Error())
	}

	c.JSON(http.StatusOK, suggestions)
}
