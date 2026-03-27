# Local Development Guide

To run the Virgo4 suggestor service locally for real-time testing and development, follow these steps.

## 1. Prerequisites
- **Go 1.25+**: Ensure Go is installed and in your PATH.
- **AWS Credentials**: You need access to Amazon Bedrock and the UVA Knowledge Base (ID: `ANITQDQQXN`).
- **Solr Access**: The service needs to reach the Solr autocomplete core.

## 2. Environment Configuration

The service loads its primary configuration from an environment variable named `VIRGO4_SUGGESTOR_WS_JSON_01`.

### Create a local config file (e.g., `local.json`)
```json
{
  "service": {
    "port": "8080",
    "jwt_key": "local-dev-key"
  },
  "solr": {
    "host": "http://localhost:8080/solr",
    "core": "autocomplete",
    "clients": {
      "service": {
        "endpoint": "select",
        "conn_timeout": "5",
        "read_timeout": "5"
      },
      "healthcheck": {
        "endpoint": "admin/ping",
        "conn_timeout": "1",
        "read_timeout": "1"
      }
    }
  },
  "ai": {
    "provider": "bedrock",
    "model": "nvidia.nemotron-nano-9b-v2"
  },
  "suggestions": {
    "author": {
      "limit": 5,
      "params": {
        "deftype": "edismax",
        "fq": [
          "type:author",
          "count:[2 TO *]"
        ],
        "fl": [
          "phrase",
          "count",
          "score"
        ],
        "qf": "matchFullWords phonetic",
        "sort": "score desc, count desc"
      }
    }
  }
}
```

> [!NOTE]
> If you are connecting to the staging Solr instance from outside the network, you may need to SSH tunnel it to localhost: `ssh -L 8080:virgo4-solr-staging-replica-private.internal.lib.virginia.edu:8080 your-user@your-gateway`

## 3. Running the Service

You can run the service using `go run` or by building it first.

### Option A: Direct Run
```bash
export VIRGO4_SUGGESTOR_WS_JSON_01=$(cat local.json)
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1

go run cmd/*.go
```

### Option B: Build and Run
```bash
go build -o suggestor cmd/*.go
./suggestor
```

## 4. Testing Nearby

Once the service is running, you can test it using `curl`:

```bash
curl -X POST http://localhost:8080/api/suggest \
     -H "Content-Type: application/json" \
     -d '{"query": "the singularity"}'
```

You should see the `[AGENT]` logs in your terminal as the AI performs its research turns!
