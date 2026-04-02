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
	Port   string `json:"port,omitempty"`
	JWTKey string `json:"jwt_key,omitempty"`
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

type serviceConfigAI struct {
	Provider         string `json:"provider,omitempty"`
	Key              string `json:"key,omitempty"`
	URL              string `json:"url,omitempty"`
	Model            string `json:"model,omitempty"`
	KnowledgeBaseID  string `json:"knowledge_base_id,omitempty"`
	GuardrailID      string `json:"guardrail_id,omitempty"`
	GuardrailVersion string `json:"guardrail_version,omitempty"`
}

type serviceConfig struct {
	Service     serviceConfigService         `json:"service,omitempty"`
	Solr        serviceConfigSolr            `json:"solr,omitempty"`
	Suggestions serviceConfigSuggestionTypes `json:"suggestions,omitempty"`
	AI          serviceConfigAI              `json:"ai,omitempty"`
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

	if cfg.Solr.Host == "" {
		cfg.Solr.Host = "http://virgo4-solr-staging-replica-private.internal.lib.virginia.edu:8080"
	}
	if cfg.Solr.Core == "" {
		cfg.Solr.Core = "autocomplete"
	}
	if cfg.Solr.Clients.Service.Endpoint == "" {
		cfg.Solr.Clients.Service.Endpoint = "select"
	}
	if cfg.Solr.Clients.HealthCheck.Endpoint == "" {
		cfg.Solr.Clients.HealthCheck.Endpoint = "admin/ping"
	}

	if host := os.Getenv(envPrefix + "_SOLR_HOST"); host != "" {
		cfg.Solr.Host = host
	}

	if port := os.Getenv(envPrefix + "_PORT"); port != "" {
		cfg.Service.Port = port
	}

	if cfg.Service.Port == "" {
		cfg.Service.Port = "8080"
	}

	if kbID := os.Getenv(envPrefix + "_BEDROCK_KB_ID"); kbID != "" {
		cfg.AI.KnowledgeBaseID = kbID
	}
	if grID := os.Getenv(envPrefix + "_GUARDRAIL_ID"); grID != "" {
		cfg.AI.GuardrailID = grID
	}
	if grVer := os.Getenv(envPrefix + "_GUARDRAIL_VERSION"); grVer != "" {
		cfg.AI.GuardrailVersion = grVer
	}

	// Default AI config if not provided
	if cfg.AI.Provider == "" {
		cfg.AI.Provider = "bedrock"
	}
	if cfg.AI.Model == "" {
		cfg.AI.Model = "google.gemma-3-4b-it"
	}
	if cfg.AI.KnowledgeBaseID == "" {
		cfg.AI.KnowledgeBaseID = "ANITQDQQXN"
	}
	if cfg.AI.GuardrailID == "" {
		cfg.AI.GuardrailID = "sii0rl6seb24"
	}
	if cfg.AI.GuardrailVersion == "" {
		cfg.AI.GuardrailVersion = "1"
	}

	log.Printf("[CONFIG] AI config: Provider=%s, Model=%s, KB=%s, Guardrail=%s:%s", 
		cfg.AI.Provider, cfg.AI.Model, cfg.AI.KnowledgeBaseID, cfg.AI.GuardrailID, cfg.AI.GuardrailVersion)

	bytes, err := json.Marshal(cfg)
	if err != nil {
		log.Printf("error encoding config json: %s", err.Error())
		os.Exit(1)
	}

	log.Printf("[CONFIG] composite json:\n%s", string(bytes))

	return &cfg
}
