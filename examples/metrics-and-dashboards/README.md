# Metrics and Dashboards

An example to demonstrate using Prometheus and Grafana to view go-libp2p
metrics. Sets up a Prometheus server and Grafana server via Docker compose. A
small go-libp2p dummy application is included to emit metrics.

Run it with:

```
docker compose -f ../../dashboards/docker-compose.base.yml -f ./compose.yml up
```

Go to http://localhost:3000/dashboards to see the dashboards.
