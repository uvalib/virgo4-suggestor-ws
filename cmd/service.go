package main

import (
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gin-gonic/gin"
)

// ServiceContext contains common data used by all handlers
type ServiceContext struct {
}

// InitializeService will initialize the service context based on the config parameters.
func InitializeService(cfg *ServiceConfig) *ServiceContext {
	log.Printf("Initializing Service")
	svc := ServiceContext{}

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

	/*
		if err := s.handlePingRequest(); err != nil {
			status = http.StatusInternalServerError
			hcSolr = hcResp{Healthy: false, Message: err.Error()}
		}
	*/

	hcMap["solr"] = hcSolr

	c.JSON(status, hcMap)
}
