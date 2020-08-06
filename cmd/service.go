package main

import (
	"errors"
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
	"github.com/uvalib/virgo4-jwt/v4jwt"
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

func httpClientWithTimeouts(conn, read string) *http.Client {
	connTimeout := integerWithMinimum(conn, 1)
	readTimeout := integerWithMinimum(read, 1)

	client := &http.Client{
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

	return client
}

// InitializeService will initialize the service context based on the config parameters.
func InitializeService(cfg *serviceConfig) *ServiceContext {
	log.Printf("initializing service")

	serviceCtx := ServiceSolrContext{
		url:    fmt.Sprintf("%s/%s/%s", cfg.Solr.Host, cfg.Solr.Core, cfg.Solr.Clients.Service.Endpoint),
		client: httpClientWithTimeouts(cfg.Solr.Clients.Service.ConnTimeout, cfg.Solr.Clients.Service.ReadTimeout),
	}

	healthCtx := ServiceSolrContext{
		url:    fmt.Sprintf("%s/%s/%s", cfg.Solr.Host, cfg.Solr.Core, cfg.Solr.Clients.HealthCheck.Endpoint),
		client: httpClientWithTimeouts(cfg.Solr.Clients.HealthCheck.ConnTimeout, cfg.Solr.Clients.HealthCheck.ReadTimeout),
	}

	solr := ServiceSolr{
		service:     serviceCtx,
		healthcheck: healthCtx,
	}

	svc := ServiceContext{
		config: cfg,
		solr:   solr,
	}

	log.Printf("[SERVICE] solr service url     = [%s]", serviceCtx.url)
	log.Printf("[SERVICE] solr healthcheck url = [%s]", healthCtx.url)

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
	s := InitializeSuggestion(svc, c)

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
	s := InitializeSuggestion(svc, c)

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
	s := InitializeSuggestion(svc, c)

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

func getBearerToken(authorization string) (string, error) {
	components := strings.Split(strings.Join(strings.Fields(authorization), " "), " ")

	// must have two components, the first of which is "Bearer", and the second a non-empty token
	if len(components) != 2 || components[0] != "Bearer" || components[1] == "" {
		return "", fmt.Errorf("invalid Authorization header: [%s]", authorization)
	}

	token := components[1]

	if token == "undefined" {
		return "", errors.New("bearer token is undefined")
	}

	return token, nil
}

// AuthenticateHandler ensures the request contains a valid token
func (svc *ServiceContext) AuthenticateHandler(c *gin.Context) {
	token, err := getBearerToken(c.GetHeader("Authorization"))
	if err != nil {
		log.Printf("authentication failed: [%s]", err.Error())
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	claims, err := v4jwt.Validate(token, svc.config.Service.JWTKey)

	if err != nil {
		log.Printf("JWT signature for %s is invalid: %s", token, err.Error())
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	c.Set("claims", claims)
}

// AdminHandler ensures the token is for an admin user
func (svc *ServiceContext) AdminHandler(c *gin.Context) {
	val, ok := c.Get("claims")

	if ok == false {
		log.Printf("no claims")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	claims := val.(*v4jwt.V4Claims)

	if claims.Role.String() != "admin" {
		log.Printf("insufficient permissions")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
}
