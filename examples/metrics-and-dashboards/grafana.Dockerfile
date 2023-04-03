FROM grafana/grafana

COPY ./dashboards /etc/grafana/dashboards
USER root

# Replace every instrance of ${DS_PROMETHEUS} with "prometheusuid"
# Grafana doesn't let us do this in provisioning, so we do it ourselves.
RUN sed -i 's/${DS_PROMETHEUS}/prometheusuid/g' /etc/grafana/dashboards/*/*.json 
RUN cat /etc/grafana/dashboards/swarm/swarm.json
USER grafana
