FROM python:3.13-slim
WORKDIR /app
COPY services/search/search.py .
ENV HOST=0.0.0.0 PORT=8082 CORE_URL=http://core:8081
EXPOSE 8082
USER nobody
CMD ["python", "search.py"]
