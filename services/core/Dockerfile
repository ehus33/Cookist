FROM gradle:8.13-jdk17 AS build
WORKDIR /build
COPY services/core/ .
RUN gradle --no-daemon installDist

FROM eclipse-temurin:17-jre
WORKDIR /app
COPY --from=build /build/build/install/cookist-core/ /app/core/
COPY shared/catalog.json /app/shared/catalog.json
ENV HOST=0.0.0.0 PORT=8081 COOKIST_DB=/app/data/cookist.db COOKIST_CATALOG=/app/shared/catalog.json
RUN mkdir -p /app/data && chown -R 10001:10001 /app
USER 10001
VOLUME ["/app/data"]
EXPOSE 8081
ENTRYPOINT ["/app/core/bin/cookist-core"]
