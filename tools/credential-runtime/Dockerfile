# Build context `tosub2` is pinned to 8548397e89bf80e508eda64a87e0d556d43abc84.
FROM node:20-bookworm-slim AS node
RUN npm install --prefix /opt/tosub2 --omit=dev --ignore-scripts --no-audit --no-fund jsdom@26.1.0
FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /opt/tosub2/node_modules /opt/tosub2/node_modules
WORKDIR /app
COPY worker.py /app/worker.py
COPY --from=tosub2 src /opt/tosub2/src
COPY --from=tosub2 LICENSE /opt/tosub2/LICENSE
RUN useradd --create-home --uid 10001 worker && chown -R worker:worker /app /opt/tosub2
USER worker
ENV TOSUB2_ROOT=/opt/tosub2 PYTHONUNBUFFERED=1
ENTRYPOINT ["python", "/app/worker.py"]
