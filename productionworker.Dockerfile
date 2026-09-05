# Production worker: job creation, model rendering, batch planning, dispatch
# and the Shopify order pull - the whole pipeline, off the API's host.
#
# It differs from Dockerfile in exactly one way that matters: OpenSCAD. The API
# image is distroless/static, which has no shell and no packages, so a render
# there is impossible - model generation reports itself disabled at startup and
# every personalised job is held for a manual upload. This image carries the
# binary that makes the pipeline able to finish a plank.
#
# No ports: it consumes River queues from Postgres and reads and writes object
# storage. Scale it with more replicas; River locks each job to one worker, and
# the leader-gated periodic jobs still fire exactly once across all of them.
#
#   docker build -f productionworker.Dockerfile -t tensor-production-worker .

# --- Build stage ---------------------------------------------------------------
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/productionworker ./cmd/productionworker

# --- Runtime stage -------------------------------------------------------------
FROM debian:12-slim
ENV DEBIAN_FRONTEND=noninteractive

# openscad-nogui, not openscad: the GUI package pulls in X11 and Qt for a
# binary that only ever runs headless with -o.
RUN apt-get update && apt-get install -y --no-install-recommends \
      openscad-nogui ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/productionworker /usr/local/bin/productionworker

# Unprivileged, matching the API and slice-worker images. OpenSCAD writes its
# temporary .scad and .stl through a writable HOME.
RUN useradd --create-home --uid 10001 worker
WORKDIR /app
RUN chown worker:worker /app
USER worker
ENV HOME=/home/worker
CMD ["productionworker"]
