# SAM subject-select service (MobileSAM via onnxruntime, CPU).
# Weights download into the /models volume on first start — see entrypoint.sh.
FROM python:3.11-slim

RUN pip install --no-cache-dir onnxruntime==1.19.2 numpy==2.4.6 pillow==12.3.0 requests==2.34.2

WORKDIR /app
COPY app.py entrypoint.sh ./
RUN chmod +x entrypoint.sh

VOLUME /models
EXPOSE 8900
ENTRYPOINT ["./entrypoint.sh"]
