# Configure Suggestor Service
# Set Solr Config (Host, Core, Clients)
# Set Solr Config (Using Catalog Pool as the "Solr" source)
# The "Solr" client in this code seems to speak the Virgo Pool API (params in JSON)
export VIRGO4_SUGGESTOR_WS_JSON_SOLR='{"solr": {"host":"https://pool-solr-ws-catalog-dev.internal.lib.virginia.edu", "core":"api", "clients":{"service":{"endpoint":"search"}, "healthcheck":{"endpoint":"ping"}}}}'
# Configure Bedrock
export VIRGO4_SUGGESTOR_WS_JSON_AI='{"ai": {"provider":"bedrock", "model":"anthropic.claude-3-sonnet-20240229-v1:0"}}'
# Configure Port (avoid 8080 collision)
export VIRGO4_SUGGESTOR_WS_JSON_SERVICE='{"service": {"port":"8085"}}'
# Run the binary
./bin/app