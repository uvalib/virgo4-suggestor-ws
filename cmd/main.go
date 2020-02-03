package main

import (
	"fmt"
	"log"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	ginprometheus "github.com/zsais/go-gin-prometheus"
)

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

/*
	router.GET("/favicon.ico", svc.ignoreHandler)

	router.GET("/version", svc.versionHandler)
	router.GET("/identify", svc.identifyHandler)
	router.GET("/healthcheck", svc.healthCheckHandler)

	if api := router.Group("/api"); api != nil {
		api.POST("/suggest", svc.authenticateHandler, svc.suggestHandler)
	}

	portStr := fmt.Sprintf(":%s", svc.config.listenPort)
*/

	portStr := fmt.Sprintf(":%s", "8641")
	log.Printf("Start service on %s", portStr)

	log.Fatal(router.Run(portStr))
}
