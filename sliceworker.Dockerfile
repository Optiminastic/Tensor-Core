# Slice worker: the Go River worker + Bambu Studio (headless, under xvfb).
#
# Replaces the Python Celery worker + FastAPI dispatcher. The heavy runtime
# (Bambu AppImage + xvfb + GUI libs) is unchanged from slicer-worker/Dockerfile;
# only the app layer swaps from a Python venv to a single static Go binary.
#
# Build context is Tensor-Core/ (needs go.mod and the whole module):
#   docker build -f sliceworker.Dockerfile -t tensor-slice-worker .

# --- Build stage: compile the worker binary -------------------------------------
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary so it runs on the bare Ubuntu runtime without a Go toolchain.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/sliceworker ./cmd/sliceworker

# --- Runtime stage: Bambu Studio + the worker binary ----------------------------
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive

# xvfb for the headless display and the GTK/WebKit/GL/GStreamer libs the Bambu
# Studio binary links against. No Python this time.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl xvfb file unzip \
      libgtk-3-0 libwebkit2gtk-4.1-0 \
      libgl1 libglu1-mesa libegl1 libosmesa6 libgl1-mesa-dri \
      libgstreamer1.0-0 libgstreamer-plugins-base1.0-0 \
      libsecret-1-0 libsoup-3.0-0 libnotify4 libwebkitgtk-6.0-4 \
      libglib2.0-0 libdbus-1-3 fonts-dejavu-core locales \
      libfuse2t64 \
    && rm -rf /var/lib/apt/lists/*

RUN locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# Extract the Bambu Studio AppImage (GPL/AGPL - bundling is permitted).
WORKDIR /opt/bambu
ARG APPIMAGE_URL=https://github.com/bambulab/BambuStudio/releases/download/v02.07.01.62/BambuStudio_ubuntu24.04-v02.07.01.62-20260616195227.AppImage
RUN curl -fL -o bambu.AppImage "$APPIMAGE_URL" \
    && chmod +x bambu.AppImage \
    && (./bambu.AppImage --appimage-extract || true) \
    && test -d squashfs-root \
    && rm -f bambu.AppImage
ENV BAMBU_ROOT=/opt/bambu/squashfs-root

COPY --from=build /out/sliceworker /usr/local/bin/sliceworker

WORKDIR /app
CMD ["sliceworker"]
