FROM golang:1.24-alpine AS build
WORKDIR /src
COPY services/gateway/ .
RUN go test ./... && CGO_ENABLED=0 go build -o /gateway .
FROM hashicorp/terraform:1.9.8 AS terraform
FROM node:24-alpine AS web
WORKDIR /src
ARG VITE_AUTH_MODE=demo
ARG VITE_COGNITO_ISSUER
ARG VITE_COGNITO_CLIENT_ID
ARG VITE_COGNITO_DOMAIN
COPY package.json yarn.lock ./
COPY apps/web/package.json apps/web/package.json
RUN yarn install --frozen-lockfile --non-interactive
COPY apps/web apps/web
COPY services/gateway/site_styles.css services/gateway/site_styles.css
RUN yarn build
FROM alpine:3.21
WORKDIR /app
COPY --from=build /gateway /usr/local/bin/gateway
COPY --from=terraform /bin/terraform /usr/local/bin/terraform
COPY --from=web /src/apps/web/dist /app/web
RUN mkdir -p /app/deployments && chown 65534:65534 /app/deployments && chmod 700 /app/deployments
ENV HOST=0.0.0.0 PORT=8080 CORE_URL=http://core:8081 WEB_DIST=/app/web
EXPOSE 8080
USER nobody
CMD ["gateway"]
