FROM python:3.12-slim-bookworm
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY otel.py server.py ./
ENV OTEL_LOGS_EXPORTER=none
ENV PYTHONUNBUFFERED=1
EXPOSE 3000 9464
CMD ["python", "server.py"]
