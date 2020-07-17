package main

import (
	"fmt"
	"log"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ginprometheus "github.com/zsais/go-gin-prometheus"
)

// GitCommit is the git commit for this build; supplied at compile time
var GitCommit string

/**
 * Main entry point for the web service
 */
func main() {
	log.Printf("===> virgo4-suggestor-ws starting up <===")

	cfg := loadConfig()
	svc := InitializeService(cfg)

	gin.SetMode(gin.ReleaseMode)
	//gin.DisableConsoleColor()

	router := gin.Default()

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

	router.GET("/favicon.ico", svc.IgnoreHandler)

	router.GET("/version", svc.VersionHandler)
	router.GET("/healthcheck", svc.HealthCheckHandler)

	if api := router.Group("/api"); api != nil {
		api.POST("/suggest", svc.SuggestionHandler)
		api.POST("/suggest/authors", svc.AuthorSuggestionHandler)
	}

	portStr := fmt.Sprintf(":%s", cfg.Service.Port)
	log.Printf("Start service on %s", portStr)

	log.Fatal(router.Run(portStr))
}
