FROM gradle:8.13-jdk17 AS build
WORKDIR /build
COPY services/core/ .
RUN gradle --no-daemon installDist

FROM eclipse-temurin:17-jre
WORKDIR /app
COPY --from=build /build/build/install/cookist-core/ /app/core/
ENV HOST=0.0.0.0 PORT=8081
USER 10001
EXPOSE 8081
ENTRYPOINT ["/app/core/bin/cookist-core"]
