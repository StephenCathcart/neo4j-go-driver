# opentelemetry spike

## Run demo REST server
`cd otel-demo &&  go run ./cmd/server`

## Open `nodes-java-demo` project
`docker compose -f docker-compose/docker-compose.yaml up -d`

`docker compose -f docker-compose/docker-compose.yaml down`

## Trigger
Cause server to fire cypher query and OT call
`http://localhost:8080/movies`

## Grafana

View data: Explore -> Search -> Service Name = "nodes-go-demo" -> Run Query (can take a minute to see data)
`http://localhost:3000/explore`

