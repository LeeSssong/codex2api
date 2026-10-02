# Build context `tosub2` is pinned to 8548397e89bf80e508eda64a87e0d556d43abc84.
FROM node:20-bookworm-slim AS node
RUN npm install --prefix /opt/tosub2 --omit=dev --ignore-scripts --no-audit --no-fund jsdom@26.1.0
FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir curl_cffi==0.15.0
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /opt/tosub2/node_modules /opt/tosub2/node_modules
WORKDIR /app
COPY worker.py /app/worker.py
COPY engines.py /app/engines.py
COPY --from=tosub2 src /opt/tosub2/src
COPY --from=tosub2 LICENSE /opt/tosub2/LICENSE
COPY --from=turb core /opt/turb/core
COPY --from=turb config /opt/turb/config
COPY --from=turb sentinel /opt/turb/sentinel
COPY --from=turb LICENSE /opt/turb/LICENSE
COPY SOURCE-LICENSE /app/SOURCE-LICENSE
RUN useradd --create-home --uid 10001 worker && chown -R worker:worker /app /opt/tosub2 /opt/turb
USER worker
ENV TOSUB2_ROOT=/opt/tosub2 TURB_ROOT=/opt/turb TOSUB2_PYTHON=/usr/local/bin/python PYTHONUNBUFFERED=1
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD ["python", "/app/worker.py", "--healthcheck"]
ENTRYPOINT ["python", "/app/worker.py"]
