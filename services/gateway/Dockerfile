FROM golang:1.24-alpine AS build
WORKDIR /src
COPY services/gateway/ .
RUN go test ./... && CGO_ENABLED=0 go build -o /gateway .
FROM node:24-alpine AS web
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/package.json
RUN npm ci
COPY apps/web apps/web
COPY shared shared
RUN npm run build
FROM alpine:3.21
WORKDIR /app
COPY --from=build /gateway /usr/local/bin/gateway
COPY --from=web /src/apps/web/dist /app/web
ENV HOST=0.0.0.0 PORT=8080 CORE_URL=http://core:8081 SEARCH_URL=http://search:8082 WEB_DIST=/app/web
EXPOSE 8080
USER nobody
CMD ["gateway"]
