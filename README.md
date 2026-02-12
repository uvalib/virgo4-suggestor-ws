# Virgo4 search suggestor service

This is the search suggestor service for Virgo4.  It aims to provide alternate
search suggestions based on keyword searches.  For example, someone might perform
a keyword search for an author, but does not want to see books about that author,
only books by that author.  This service can recognize the keyword as potential
author(s), and provide high-confidence author search suggestions to the user.

This service requires a json config string to be set in an environment variable. A setup
command has been included to parse terraform config and generate the required data.
Instructions for running can be found in /setup/READE.md

### System Requirements
* GO version 1.12 or greater (mod required)

### Current API

* GET /version : return service version info
* GET /healthcheck : test health of system components; results returned as JSON.
* GET /metrics : returns Prometheus metrics
* POST /api/suggest : suggest alternate searches for a given search
