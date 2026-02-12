package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
)

type terraformConfig struct {
	Service struct {
		Port   string `json:"port"`
		JwtKey string `json:"jwt_key"`
	} `json:"service"`
	Solr any `json:"solr"`
	Ai   struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"ai"`
	Suggestions any `json:"suggestions"`
}

func main() {
	type cfgData struct {
		File   string
		EnvVar string
	}

	var terformBase string
	var tgtEnv string
	var aiProvider string
	var aiModel string
	var port string
	flag.StringVar(&terformBase, "dir", "", "local dirctory for virgo4.lib.virginia.edu/ecs-tasks")
	flag.StringVar(&tgtEnv, "env", "staging", "production or staging")
	flag.StringVar(&aiProvider, "provider", "", "ai provider name")
	flag.StringVar(&aiModel, "model", "google.gemma-3-4b-it", "ai model")
	flag.StringVar(&port, "port", "8080", "port to run the pool on")
	flag.Parse()

	if terformBase == "" {
		log.Fatal("dir is required")
	}
	if tgtEnv != "staging" && tgtEnv != "production" {
		log.Fatal("env must be staging or production")
	}

	cfgBase := path.Join(terformBase, tgtEnv, "suggestor-ws/config")

	log.Printf("Generate suggestor config for %s from %s", tgtEnv, cfgBase)
	cfgFile := cfgData{File: "service.json", EnvVar: "VIRGO4_SUGGESTOR_WS_JSON_01_SERVICE"}

	tgtFile := path.Join(cfgBase, cfgFile.File)
	jsonBytes, err := os.ReadFile(tgtFile)
	if err != nil {
		log.Fatal(err.Error())
	}

	// Parse into a confg structre and sub in values; then convert to a flat string
	var parsed terraformConfig
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		log.Fatal(err.Error())
	}
	parsed.Service.Port = port
	if aiProvider != "" {
		parsed.Ai.Provider = aiProvider
	}
	if aiModel != "" {
		parsed.Ai.Model = aiModel
	}
	flatBytes, _ := json.Marshal(parsed)
	outEnv := fmt.Sprintf("export %s='%s'", cfgFile.EnvVar, flatBytes)

	outF, err := os.Create("setup_env.sh")
	if err != nil {
		log.Fatal(err.Error())
	}
	outF.WriteString("#!/bin/bash\n\n")
	fmt.Fprintf(outF, "export VIRGO4_SUGGESTOR_WS_SOLR_HOST=http://virgo4-solr-%s-replica-private.internal.lib.virginia.edu:8080/solr\n", tgtEnv)
	outF.WriteString(outEnv)
	outF.Close()
	os.Chmod("setup_env.sh", 0777)
}
