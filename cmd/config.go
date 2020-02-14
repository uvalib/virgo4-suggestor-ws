package main

import (
	"log"
	"os"
)

// ServiceConfig defines all of the service configuration parameters
type ServiceConfig struct {
	ListenPort         string
	SolrHost           string
	SolrCore           string
	SolrHandler        string
	SolrConnTimeout    string
	SolrReadTimeout    string
	SolrScoreThreshold string
}

func ensureSet(env string) string {
	val, set := os.LookupEnv(env)

	if set == false {
		log.Printf("environment variable not set: [%s]", env)
		os.Exit(1)
	}

	return val
}

func ensureSetAndNonEmpty(env string) string {
	val := ensureSet(env)

	if val == "" {
		log.Printf("environment variable set but empty: [%s]", env)
		os.Exit(1)
	}

	return val
}

// LoadConfiguration will load the service configuration from env/cmdline
// and return a pointer to it. Any failures are fatal.
func LoadConfiguration() *ServiceConfig {

	log.Printf("Loading configuration...")

	var cfg ServiceConfig

	cfg.ListenPort = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_LISTEN_PORT")
	cfg.SolrHost = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_HOST")
	cfg.SolrCore = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_CORE")
	cfg.SolrHandler = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_HANDLER")
	cfg.SolrConnTimeout = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_CONN_TIMEOUT")
	cfg.SolrReadTimeout = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_READ_TIMEOUT")
	cfg.SolrScoreThreshold = ensureSetAndNonEmpty("VIRGO4_SUGGESTOR_WS_SOLR_SCORE_THRESHOLD")

	log.Printf("[CONFIG] ListenPort         = [%s]", cfg.ListenPort)
	log.Printf("[CONFIG] SolrHost           = [%s]", cfg.SolrHost)
	log.Printf("[CONFIG] SolrCore           = [%s]", cfg.SolrCore)
	log.Printf("[CONFIG] SolrHandler        = [%s]", cfg.SolrHandler)
	log.Printf("[CONFIG] SolrConnTimeout    = [%s]", cfg.SolrConnTimeout)
	log.Printf("[CONFIG] SolrReadTimeout    = [%s]", cfg.SolrReadTimeout)
	log.Printf("[CONFIG] SolrScoreThreshold = [%s]", cfg.SolrScoreThreshold)

	return &cfg
}
