# Build the CSS with the Tailwind standalone binary, so the image needs no Node
# at build time and nothing at all at runtime.
FROM --platform=$BUILDPLATFORM alpine:3.20 AS css
ARG TAILWIND_VERSION=v4.3.3
WORKDIR /src
# The musl build still links libstdc++/libgcc dynamically; without them it
# exits 127. The glibc build segfaults on alpine even with libc6-compat.
RUN apk add --no-cache curl libstdc++ libgcc
RUN set -eux; \
    case "$(uname -m)" in \
      x86_64)  asset=tailwindcss-linux-x64-musl ;; \
      aarch64) asset=tailwindcss-linux-arm64-musl ;; \
      *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/${asset}"; \
    chmod +x /usr/local/bin/tailwindcss
COPY web ./web
RUN tailwindcss -i ./web/css/input.css -o ./web/static/css/app.css --minify

FROM golang:1.26-alpine AS build
WORKDIR /src
# Cache the module graph separately from the source so code edits do not
# re-download dependencies.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/web/static/css/app.css ./web/static/css/app.css
ARG VERSION=dev
# CGO off and a stripped binary: this has to run in a scratch image and stay small.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/circe-server ./cmd/server

FROM scratch
# Only three things are needed at runtime: the binary, CA roots for the GitHub
# API, and zoneinfo for timestamp formatting.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/
COPY --from=build /out/circe-server /circe-server

ENV ZONEINFO=/usr/local/go/lib/time/zoneinfo.zip \
    CIRCE_DATA=/data
# Templates and static assets are embedded in the binary, so there is nothing
# else to copy and no working directory to set up.
#
# No VOLUME instruction: Railway rejects it and manages persistence itself.
# Mount a volume at /data on whichever platform you deploy to.
EXPOSE 8080
ENTRYPOINT ["/circe-server"]
