# Retention sweeper: the one process in Tensor whose job is to DELETE.
#
# Its own image and its own container, for blast radius rather than packaging.
# Stop this and nothing else changes - orders keep importing, plates keep
# slicing, the floor keeps printing, and the only consequence is that history
# stops being pruned. Run it inside the API and an API restart becomes a
# half-finished sweep, which is the one failure mode worth engineering away.
#
# Nothing heavy in here: no OpenSCAD, no mailbox, no ports. It reads Postgres,
# writes a summary per trading day, removes rows older than the retention
# period and deletes the stored models those rows pointed at.
#
# SAFE BY DEFAULT, AND IT TAKES TWO DECISIONS TO ARM. RETENTION_ENABLED is
# false unless set, so this container does nothing at all on arrival.
# RETENTION_DRY_RUN is true unless cleared, so the first thing it can be made
# to do is summarise each expired day and delete none of it. Every other
# switch in this system changes what Tensor does; this one changes what Tensor
# still has, and the default is to keep everything.
#
# TZDATA IS NOT NEEDED and is deliberately not installed, for the same reason
# the accounts worker beside it goes without: the trading day is a fixed
# +05:30 offset built in code rather than a zone loaded by name. A missing
# tzdata would otherwise turn every window silently into UTC and move five and
# a half hours of orders into the wrong day - which here would mean deleting
# them a day early.
#
#   docker build -f retentionworker.Dockerfile -t tensor-retention-worker .

# --- Build stage ---------------------------------------------------------------
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/retentionworker ./cmd/retentionworker

# --- Runtime stage -------------------------------------------------------------
# Static distroless: no shell, no package manager, nothing to exec into. This
# process holds database credentials and the power to remove rows, so the
# smallest possible surface is the right one.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/retentionworker /usr/local/bin/retentionworker
USER nonroot
ENTRYPOINT ["retentionworker"]
