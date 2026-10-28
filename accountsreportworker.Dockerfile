# Accounts report scheduler: the one process in Tensor that emails the shop's
# complete sales figures outside the company.
#
# Its own image and its own container, for blast radius rather than packaging -
# the same reasoning as the win-back worker beside it. Stop this and the daily
# register stops while orders keep importing, plates keep slicing and the floor
# keeps printing. Run it inside the API and an API restart becomes a missed
# report, which nobody notices until an accountant asks.
#
# Nothing heavy in here: no OpenSCAD, no object storage, no ports. It reads
# orders from Postgres, builds a spreadsheet in memory and opens one SMTP
# connection. Distroless/static is enough - and this process holds a mailbox
# password, so the smallest possible surface is the right one.
#
# SAFE BY DEFAULT: a brand is only mailed once somebody has turned the report
# on and saved a recipient, and with no SMTP credentials the worker says so at
# startup and sends nothing.
#
#   docker build -f accountsreportworker.Dockerfile -t tensor-accounts-report-worker .

# --- Build stage ---------------------------------------------------------------
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/accountsreportworker ./cmd/accountsreportworker

# --- Runtime stage -------------------------------------------------------------
# Static distroless: no shell, no package manager, nothing to exec into.
#
# TZDATA IS NOT NEEDED and is deliberately not installed. The report's noon is
# a fixed +05:30 offset built in code rather than a zone loaded by name,
# precisely so this image can stay empty - a missing tzdata would otherwise
# turn every window silently into UTC and move a day's orders onto the wrong
# report.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/accountsreportworker /usr/local/bin/accountsreportworker
USER nonroot
ENTRYPOINT ["accountsreportworker"]
