FROM node:22.22.2-bookworm-slim AS node
FROM python:3.12.12-slim-bookworm AS build
ARG TOSUB2_COMMIT=8548397e89bf80e508eda64a87e0d556d43abc84
ARG TURB_COMMIT=d32e49e623dddf71b5fa6f0f5b0250bef963bdbd
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 curl && rm -rf /var/lib/apt/lists/*
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm
RUN mkdir -p /build/tosub2 /build/turb && curl -fsSL --retry 3 "https://codeload.github.com/poxiao33/toSub2/tar.gz/$TOSUB2_COMMIT" -o /tmp/tosub2.tar.gz && tar -xzf /tmp/tosub2.tar.gz --strip-components=1 -C /build/tosub2 && curl -fsSL --retry 3 "https://codeload.github.com/myfanhua/turb-gpt-free-register/tar.gz/$TURB_COMMIT" -o /tmp/turb.tar.gz && tar -xzf /tmp/turb.tar.gz --strip-components=1 -C /build/turb
WORKDIR /build/tosub2
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY requirements.txt /build/requirements.txt
RUN python -m pip install --no-cache-dir --only-binary=:all: -r /build/requirements.txt
COPY . /build/tools/
WORKDIR /build/tools
RUN EXCEL_TEST_TOSUB2_ROOT=/build/tosub2 python -m unittest discover -p 'test*.py' && node --test test_openai_totp_journal.mjs test_openai_excel_password_flow.mjs && python package.py /out
FROM scratch AS artifact
COPY --from=build /out/ /
FROM python:3.12.12-slim-bookworm AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /opt/credential-runtime/
RUN useradd --create-home --uid 10001 credential && install -d -m 0700 -o credential -g credential /data/credential-operations-recovery && chown -R credential:credential /data
USER credential
WORKDIR /opt/credential-runtime
ENV TOSUB2_ROOT=/opt/credential-runtime/tosub2 TURB_ROOT=/opt/credential-runtime/turb TOSUB2_PYTHON=/opt/credential-runtime/python NODE_EXECUTABLE=/opt/credential-runtime/node OPENAI_TOTP_JOURNAL_DIR=/data/credential-operations-recovery PYTHONUNBUFFERED=1
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD ["/opt/credential-runtime/python", "/opt/credential-runtime/worker.py", "--healthcheck"]
ENTRYPOINT ["/opt/credential-runtime/python", "/opt/credential-runtime/worker.py"]
