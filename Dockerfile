# Shy Garlic — one static binary, an image without an operating system.
# Goal from the SPEC: under 25 MB. The frontend, icons and templates live in embed.FS,
# so the image has neither node_modules nor any files to lose.

FROM golang:1.25-alpine AS build
WORKDIR /src

# dependencies first — the layer is cached between builds
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/shyg ./cmd/shyg

# an empty directory to copy over — distroless has neither RUN nor chown
RUN mkdir -p /empty

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shyg /shyg
COPY --from=build /src/examples/homelab.yaml /etc/shygarlic/shygarlic.yaml
# ! Without this directory the DB landed in `/`, which uid 65532 cannot write, and
# a named volume mounted on a non-existent path is created as root:root.
COPY --from=build --chown=65532:65532 /empty /data

USER nonroot:nonroot
EXPOSE 8080
ENV SHYG_CONFIG=/etc/shygarlic/shygarlic.yaml
# the DB in /data, with the icons/ and bg/ subdirectories next to it — never in one directory,
# because a wallpaper named `shygarlic.db` would overwrite the DB
ENV SHYG_DATA=/data/shygarlic.db

ENTRYPOINT ["/shyg"]
CMD ["serve", "-config", "/etc/shygarlic/shygarlic.yaml", "-addr", ":8080"]

# ── variant with git ──────────────────────────────────────────────────────────
# The distroless image has neither a shell nor git, so config commits do not
# work in it. Whoever wants them builds this target:
#   docker build --target git -t shy-garlic:git .
FROM alpine:3.21 AS git
RUN apk add --no-cache git ca-certificates tzdata && adduser -D -u 65532 nonroot \
 && mkdir -p /data && chown 65532:65532 /data
COPY --from=build /out/shyg /shyg
COPY --from=build /src/examples/homelab.yaml /etc/shygarlic/shygarlic.yaml
USER nonroot
EXPOSE 8080
ENV SHYG_CONFIG=/etc/shygarlic/shygarlic.yaml
ENV SHYG_DATA=/data/shygarlic.db
ENTRYPOINT ["/shyg"]
CMD ["serve", "-config", "/etc/shygarlic/shygarlic.yaml", "-addr", ":8080"]
