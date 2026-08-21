# Tensor-Core production worker - consumes the River jobs that turn an imported
# Shopify order into production jobs and replan batches.
#
# Deliberately a much lighter image than sliceworker.Dockerfile: this worker is
# pure Go and never shells out, so it needs no Bambu Studio, no xvfb and no
# OpenSCAD - only CA certificates for the outbound TLS to Postgres and S3.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/productionworker ./cmd/productionworker

FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/productionworker /app/productionworker
ENTRYPOINT ["/app/productionworker"]
