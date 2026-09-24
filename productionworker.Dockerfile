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
# backfillpriority ranks jobs created before priority ordering existed. Without
# it the Priority tab opens empty on a floor that has priority work in it.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/backfillpriority ./cmd/backfillpriority
# reformbeds returns locked beds to Draft so the planner can rebuild them -
# the only way to reshape beds committed under an older rule.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/reformbeds ./cmd/reformbeds

# --- Runtime stage -------------------------------------------------------------
FROM debian:12-slim
ENV DEBIAN_FRONTEND=noninteractive

# OpenSCAD, pinned to an exact build.
#
# This was bookworm's openscad 2021.01 - the conservative choice, because the
# templates are WRITTEN for it: dnp_with_no_heart.scad and its siblings carry a
# hardcoded W_TBL of glyph widths measured by extruding each letter, precisely
# "because OpenSCAD 2021.01 has no textmetrics()". Changing the renderer under
# models that already print correctly is the risk here, and it is not
# theoretical - rendering the same plank on both versions moves the LETTERING
# bounding box by ~0.22mm (the base plate is byte-identical).
#
# 2026.09.12 is taken for one reason: it can export 3MF with colour
# (-O export-3mf/color-mode=model) and, with --enable lazy-union, as one object
# per colour. That is what allows ONE render instead of the two that
# dnp_generate.go does today. It does NOT remove meshio.Write3MF - OpenSCAD
# writes neither Metadata/model_settings.config (Bambu's extruder assignment,
# which is the only thing Bambu reads) nor project_settings.config (the slot
# declaration behind ams_mapping).
#
# Pinned to a dated snapshot, never "latest": these are nightly builds, and a
# renderer that changes under the templates on a rebuild is the whole hazard
# described above happening silently.
#
# AppImage rather than a package because no distro ships anything this recent.
# Extracted rather than run in place - mounting an AppImage needs FUSE, which a
# container does not have; --appimage-extract needs nothing.
ARG OPENSCAD_VERSION=2026.09.12
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
      libgl1 libglu1-mesa libegl1 libfontconfig1 libfreetype6 libharfbuzz0b \
      `# fontconfig, not just libfontconfig1: fc-cache is what makes a font in` \
      `# fonts/ visible to OpenSCAD at all, and fc-match is how the build` \
      `# checks it landed. The old image got these free with the openscad` \
      `# package, which this no longer installs.` \
      fontconfig \
      libglib2.0-0 libx11-6 libxext6 libxrender1 libxi6 libxkbcommon0 \
      libdbus-1-3 libzip4 \
 && curl -fsSL -o /tmp/openscad.AppImage \
      "https://files.openscad.org/snapshots/OpenSCAD-${OPENSCAD_VERSION}-x86_64.AppImage" \
 && chmod +x /tmp/openscad.AppImage \
 && cd /opt && /tmp/openscad.AppImage --appimage-extract > /dev/null \
 && mv squashfs-root openscad \
 && printf '#!/bin/sh\nexec /opt/openscad/AppRun "$@"\n' > /usr/local/bin/openscad \
 && chmod 0755 /usr/local/bin/openscad \
 `# /usr/bin/openscad too, because that is where the Debian package used to` \
 `# put it and OPENSCAD_BIN is set per DEPLOYMENT, not by this repo. Coolify` \
 `# carried /usr/bin/openscad, so dropping the package silently disabled` \
 `# rendering on prod: Renderer.Available() just returns false and the worker` \
 `# holds every personalised job for a manual upload. It does not crash, and` \
 `# nothing on the floor says why. A symlink costs nothing and makes the env` \
 `# var's value stop mattering.` \
 && ln -sf /usr/local/bin/openscad /usr/bin/openscad \
 && rm /tmp/openscad.AppImage \
 && apt-get purge -y curl && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/*

# The font the templates actually name.
#
# Without this the image renders planks with two thirds of the lettering
# missing, and reports success. fontconfig does not fail on a missing family,
# it SUBSTITUTES: in this image `Segoe UI Black` resolved to DejaVu Sans, and
# the two produced byte-identical output - 11,301 mm3 of lettering against the
# 33,676 mm3 the real face gives. That is the difference between the planks on
# the floor and the thin ones a re-render produced.
#
# Every W_TBL entry in the templates is a glyph width measured from this face,
# so the font is not a preference - it is the thing those numbers describe. A
# different face makes the auto-fit squeeze by the wrong amount.
#
# The file IS committed, deliberately. The image is built from the repo by
# Coolify, so a font that is not in the repo is a font prod does not have -
# and a prod worker without it renders every plank thin while reporting
# success. Segoe UI ships with Windows and its licence does not contemplate
# redistribution in a container image; that was raised and accepted as a
# business decision. fonts/README.md records the free alternatives if it ever
# has to come back out.
COPY fonts/ /usr/share/fonts/truetype/tensor/
RUN fc-cache -f > /dev/null \
 && fc-match "Segoe UI Black" | tee /tmp/font-check \
 && grep -qi "seguibl\|Segoe" /tmp/font-check \
    || echo "WARNING: Segoe UI Black is NOT installed - lettering will render ~66% thin"

COPY --from=build /out/productionworker /usr/local/bin/productionworker
COPY --from=build /out/rerender /usr/local/bin/rerender
COPY --from=build /out/replate /usr/local/bin/replate
COPY --from=build /out/backfillpriority /usr/local/bin/backfillpriority
COPY --from=build /out/reformbeds /usr/local/bin/reformbeds

# Unprivileged, matching the API and slice-worker images. OpenSCAD writes its
# temporary .scad and .stl through a writable HOME.
RUN useradd --create-home --uid 10001 worker
WORKDIR /app
RUN chown worker:worker /app
USER worker
ENV HOME=/home/worker
CMD ["productionworker"]
