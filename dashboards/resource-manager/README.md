# Ready to go Grafana Dashboard

Here are some prebuilt dashboards that you can add to your Grafana instance. To
import follow the Grafana docs [here](https://grafana.com/docs/grafana/latest/dashboards/export-import/#import-dashboard)

## Setup

Metrics are enabled by default. By default, metrics will be sent to
`prometheus.DefaultRegisterer`. To use a different Registerer use the libp2p
option `libp2p.PrometheusRegisterer`.

## Updating Dashboard json

Use the share functionality on an existing dashboard, and make sure to toggle
"Export for sharing externally". See the [Grafana
Docs](https://grafana.com/docs/grafana/latest/dashboards/export-import/#exporting-a-dashboard)
for more details.
