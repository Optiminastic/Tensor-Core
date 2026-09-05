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
# rerender ships beside the worker because it only ENQUEUES model_gen jobs -
# the worker is what actually runs OpenSCAD, so this is the one host where
# "re-render everything after a template or font change" can be run at all.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/rerender ./cmd/rerender
# replate carries a finished re-render onto the beds: a merged plate is a
# snapshot taken when the bed formed, so re-rendering the jobs behind it leaves
# every bed still holding - and still printing - the old geometry until this runs.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/replate ./cmd/replate

# --- Runtime stage -------------------------------------------------------------
FROM debian:12-slim
ENV DEBIAN_FRONTEND=noninteractive

# openscad 2021.01, which is what bookworm ships and what the templates are
# written for - dnp_with_no_heart.scad and its siblings carry an explicit note
# that 2021.01 has no textmetrics() and size their own text instead. A newer
# AppImage would be a change of renderer under models that already print
# correctly, so the distro package is the conservative choice, not a compromise.
#
# There is no openscad-nogui in Debian or Ubuntu - it does not exist in either
# archive - so this is the GUI package. It pulls Qt in, and that is only image
# size: STL export with -o needs no display, and runs here with none.
RUN apt-get update && apt-get install -y --no-install-recommends \
      openscad ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/productionworker /usr/local/bin/productionworker
COPY --from=build /out/rerender /usr/local/bin/rerender
COPY --from=build /out/replate /usr/local/bin/replate

# Unprivileged, matching the API and slice-worker images. OpenSCAD writes its
# temporary .scad and .stl through a writable HOME.
RUN useradd --create-home --uid 10001 worker
WORKDIR /app
RUN chown worker:worker /app
USER worker
ENV HOME=/home/worker
CMD ["productionworker"]
