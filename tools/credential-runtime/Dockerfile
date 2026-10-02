FROM python:3.12-alpine
WORKDIR /app
COPY worker.py .
RUN chmod 0555 worker.py
USER nobody
ENTRYPOINT ["python", "/app/worker.py"]
