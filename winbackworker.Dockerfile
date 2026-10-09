# Win-back scheduler: the one process in Tensor that contacts a member of the
# public directly, by telephone and by WhatsApp.
#
# Its own image, and its own container, for blast radius rather than for
# packaging. Stop this and the calling stops dead while orders keep importing,
# plates keep slicing and the floor keeps printing. Run it inside the API and
# an API restart becomes a calling restart, and taking calling down means
# taking the API down.
#
# Nothing heavy in here: no OpenSCAD, no object storage, no ports. It reads
# Shopify over HTTPS, talks to Sarvam and Meta, mints a one-hour discount
# code in the shop, and consumes one River queue from Postgres.
# Distroless/static is enough.
#
# SAFE BY DEFAULT: with WINBACK_CALLS_ENABLED and WINBACK_WHATSAPP_ENABLED
# unset it runs every rule, logs exactly who it would have reached on each
# channel, and contacts nobody.
#
#   docker build -f winbackworker.Dockerfile -t tensor-winback-worker .

# --- Build stage ---------------------------------------------------------------
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/winbackworker ./cmd/winbackworker

# --- Runtime stage -------------------------------------------------------------
# Static distroless: no shell, no package manager, nothing to exec into. This
# process holds a Sarvam key and a Shopify token, so the smallest possible
# surface is the right one.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/winbackworker /usr/local/bin/winbackworker
USER nonroot
ENTRYPOINT ["winbackworker"]
