# LaMa removal endpoint (spec/inference-api.md, tasks: ["inpaint"]).
# Model weights are fetched at container start into the /models volume —
# keeps the image small and survives rebuilds. See entrypoint.sh.
FROM python:3.11-slim

RUN pip install --no-cache-dir onnxruntime==1.19.2 numpy pillow requests

WORKDIR /app
COPY app.py entrypoint.sh ./
RUN chmod +x entrypoint.sh

VOLUME /models
EXPOSE 8900
ENTRYPOINT ["./entrypoint.sh"]
