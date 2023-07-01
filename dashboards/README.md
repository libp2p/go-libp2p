# Grafana Dashboards

This directory contains prebuilt dashboards (provided as JSON files) for various components.
For steps on how to import and use them [please read the official Grafana documentation.](https://grafana.com/docs/grafana/latest/dashboards/export-import/#import-dashboard)

## Using locally

For local development and debugging, it can be useful to spin up a local Prometheus and Grafana instance.

To expose metrics, we first need to expose a metrics collection endpoint. Add this to your code:

```go
import "github.com/prometheus/client_golang/prometheus/promhttp"

go func() {
    http.Handle("/debug/metrics/prometheus", promhttp.Handler())
    log.Fatal(http.ListenAndServe(":5001", nil))
}()
```

This exposes a metrics collection endpoint at http://localhost:5001/debug/metrics/prometheus. Note that this is the same endpoint that [Kubo](https://github.com/ipfs/kubo) uses, so if you want to gather metrics from Kubo, you can skip this step.

On macOS:
```bash
docker compose -f docker-compose.base.yml up
```
On Linux, dashboards can be inspected locally by running:
```bash
docker compose -f docker-compose.base.yml -f docker-compose-linux.yml up
```

and opening Grafana at http://localhost:3000.


### Making Dashboards usable with Provisioning

The following section is only relevant for creators of dashboards.

Due to a bug in Grafana, it's not possible to provision dashboards shared for external use directly. We need to apply the workaround described in https://github.com/grafana/grafana/issues/10786#issuecomment-568788499 (adding a few lines in the dashboard JSON file).
