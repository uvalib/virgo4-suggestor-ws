package main

import (
	"path/filepath"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ginprometheus "github.com/zsais/go-gin-prometheus"
)

// git commit used for this build; supplied at compile time
var gitCommit string

/**
 * Main entry point for the web service
 */
func main() {
	log.Printf("===> virgo4-suggestor-ws starting up <===")

/*
	cfg := loadConfig()
	svc := initializeService(cfg)
*/

	gin.SetMode(gin.ReleaseMode)
	//gin.DisableConsoleColor()

	router := gin.Default()

	router.Use(gzip.Gzip(gzip.DefaultCompression))

	corsCfg := cors.DefaultConfig()
	corsCfg.AllowAllOrigins = true
	corsCfg.AllowCredentials = true
	corsCfg.AddAllowHeaders("Authorization")
	router.Use(cors.New(corsCfg))

	p := ginprometheus.NewPrometheus("gin")

	// roundabout setup of /metrics endpoint to avoid double-gzip of response
	router.Use(p.HandlerFunc())
	h := promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{DisableCompression: true}))

	router.GET(p.MetricsPath, func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	})

	router.GET("/favicon.ico", ignoreHandler)

	router.GET("/version", versionHandler)
	router.GET("/healthcheck", healthCheckHandler)

/*
	if api := router.Group("/api"); api != nil {
		api.POST("/suggest", svc.authenticateHandler, svc.suggestHandler)
	}

	portStr := fmt.Sprintf(":%s", svc.config.listenPort)
*/

	portStr := fmt.Sprintf(":%s", "8641")
	log.Printf("Start service on %s", portStr)

	log.Fatal(router.Run(portStr))
}

func ignoreHandler(c *gin.Context) {
}

func buildVersion() string {
	files, _ := filepath.Glob("buildtag.*")
	if len(files) == 1 {
		return strings.Replace(files[0], "buildtag.", "", 1)
	}

	return "unknown"
}

func versionHandler(c *gin.Context) {
	type vResp struct {
		BuildVersion string `json:"build,omitempty"`
		GoVersion    string `json:"go_version,omitempty"`
		GitCommit    string `json:"git_commit,omitempty"`
	}

	ver := vResp {
		BuildVersion: buildVersion(),
		GoVersion:    fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
		GitCommit:    gitCommit,
    }

	c.JSON(http.StatusOK, ver)
}

func healthCheckHandler(c *gin.Context) {
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
