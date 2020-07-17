package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
)

const envPrefix = "VIRGO4_SUGGESTOR_WS"

type serviceConfigService struct {
	Port string `json:"port,omitempty"`
}

type serviceConfigSolrParams struct {
	DefType string   `json:"deftype,omitempty"`
	Fl      []string `json:"fl,omitempty"`
	Fq      []string `json:"fq,omitempty"`
	Qf      string   `json:"qf,omitempty"`
	Sort    string   `json:"sort,omitempty"`
}

type serviceConfigSuggestion struct {
	Limit  int                     `json:"limit,omitempty"`
	Params serviceConfigSolrParams `json:"params,omitempty"`
}

type serviceConfigSuggestionTypes struct {
	Author serviceConfigSuggestion `json:"author,omitempty"`
}

type serviceConfigSolrClient struct {
	Endpoint    string `json:"endpoint,omitempty"`
	ConnTimeout string `json:"conn_timeout,omitempty"`
	ReadTimeout string `json:"read_timeout,omitempty"`
}

type serviceConfigSolrClients struct {
	Service     serviceConfigSolrClient `json:"service,omitempty"`
	HealthCheck serviceConfigSolrClient `json:"healthcheck,omitempty"`
}

type serviceConfigSolr struct {
	Host    string                   `json:"host,omitempty"`
	Core    string                   `json:"core,omitempty"`
	Clients serviceConfigSolrClients `json:"clients,omitempty"`
}

type serviceConfig struct {
	Service     serviceConfigService         `json:"service,omitempty"`
	Solr        serviceConfigSolr            `json:"solr,omitempty"`
	Suggestions serviceConfigSuggestionTypes `json:"suggestions,omitempty"`
}

func getSortedJSONEnvVars() []string {
	var keys []string

	for _, keyval := range os.Environ() {
		key := strings.Split(keyval, "=")[0]
		if strings.HasPrefix(key, envPrefix+"_JSON_") {
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	return keys
}

func loadConfig() *serviceConfig {
	cfg := serviceConfig{}

	// json configs

	envs := getSortedJSONEnvVars()

	valid := true

	for _, env := range envs {
		log.Printf("[CONFIG] loading %s ...", env)
		if val := os.Getenv(env); val != "" {
			dec := json.NewDecoder(bytes.NewReader([]byte(val)))
			dec.DisallowUnknownFields()

			if err := dec.Decode(&cfg); err != nil {
				log.Printf("error decoding %s: %s", env, err.Error())
				valid = false
			}
		}
	}

	if valid == false {
		log.Printf("exiting due to json decode error(s) above")
		os.Exit(1)
	}

	// optional convenience override to simplify terraform config
	if host := os.Getenv(envPrefix + "_SOLR_HOST"); host != "" {
		cfg.Solr.Host = host
	}

	bytes, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("error encoding config json: %s", err.Error())
		os.Exit(1)
	}

	log.Printf("[CONFIG] composite json:\n%s", string(bytes))

	return &cfg
}
